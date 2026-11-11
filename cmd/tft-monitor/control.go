package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"maps"
	"net"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cristiansteib/tft-smarter/internal/notify"
	"github.com/cristiansteib/tft-smarter/internal/version"
)

// settings are the knobs the desktop UI can change at runtime; they persist in the user config dir.
type settings struct {
	HogCPU       float64         `json:"hogCpu"`       // % of one core
	HogMem       float64         `json:"hogMem"`       // % of total RAM
	LoadTrigger  float64         `json:"loadTrigger"`  // 1-min load per core, %
	RamTrigger   int             `json:"ramTrigger"`   // RAM used, %
	CreditStepMs int             `json:"creditStepMs"` // rolling credits: one row per step
	DwellSec     int             `json:"dwellSec"`     // seconds per hog page when both CPU and RAM hogs alternate
	ReturnSec    int             `json:"returnSec"`    // a page you opened goes back home after this long without a touch
	Disabled     map[string]bool `json:"disabled"`     // pages never shown
	Pin          string          `json:"pin"`          // page held on screen ("" = rotate)
}

func defaultSettings() settings {
	return settings{HogCPU: 40, HogMem: 8, LoadTrigger: 60, RamTrigger: 80, ReturnSec: 60, CreditStepMs: 1400, DwellSec: 12, Disabled: map[string]bool{}}
}

var (
	cfgVal atomic.Pointer[settings] // never modified once stored: setCfg stores a fresh copy
	saveMu sync.Mutex               // orders the store and the file write of concurrent setCfg calls
	skipAt atomic.Bool              // "next page" requested
)

func init() { d := defaultSettings(); cfgVal.Store(&d) }

// cfg returns the current settings. It is cheap (no copy of the map): callers may change the plain fields of their
// copy and hand it to setCfg, but must treat Disabled as read-only (clone it to change it).
func cfg() settings { return *cfgVal.Load() }

func cfgPath() string {
	d, _ := os.UserConfigDir()
	return filepath.Join(d, "tft-monitor", "settings.json")
}

func loadCfg() {
	b, err := os.ReadFile(cfgPath())
	if err != nil {
		return
	}
	c := defaultSettings()
	if json.Unmarshal(b, &c) == nil {
		if c.ReturnSec == 0 {
			c.ReturnSec = 60
		}
		if c.Disabled == nil {
			c.Disabled = map[string]bool{}
		}
		cfgVal.Store(&c)
	}
}

func setCfg(c settings) {
	c.HogCPU = clampF(c.HogCPU, 1, 800)
	c.HogMem = clampF(c.HogMem, 0.5, 100)
	c.LoadTrigger = clampF(c.LoadTrigger, 5, 400)
	c.RamTrigger = min(max(c.RamTrigger, 10), 100)
	c.CreditStepMs = min(max(c.CreditStepMs, 300), 5000)
	c.DwellSec = min(max(c.DwellSec, 2), 120)
	c.ReturnSec = min(max(c.ReturnSec, 15), 600)
	c.Disabled = maps.Clone(c.Disabled) // the stored map is shared by every cfg() copy: it must never change
	if c.Disabled == nil {
		c.Disabled = map[string]bool{}
	}
	saveMu.Lock()
	defer saveMu.Unlock()
	cfgVal.Store(&c)
	if b, err := json.MarshalIndent(c, "", "  "); err == nil {
		os.MkdirAll(filepath.Dir(cfgPath()), 0o755)
		os.WriteFile(cfgPath(), b, 0o644)
	}
}

func clampF(v, lo, hi float64) float64 { return min(max(v, lo), hi) }

// ---- live state published by the main loop ----

type stateDoc struct {
	Connected bool      `json:"connected"`
	Updated   time.Time `json:"updated"`
	Gauges    struct {
		CPU      int        `json:"cpu"`
		RAM      int        `json:"ram"`
		Swap     int        `json:"swap"`
		CPUT     int        `json:"cpuT"`
		GPU      int        `json:"gpu"`
		GPUT     int        `json:"gpuT"`
		VRAM     int        `json:"vram"`
		GPUW     int        `json:"gpuW"`
		GPULimit int        `json:"gpuLimit"`
		NVMeT    int        `json:"nvmeT"`
		Latency  int        `json:"latency"`
		Down     int        `json:"down"` // kbit/s
		Up       int        `json:"up"`
		Uptime   uint32     `json:"uptime"`
		Load     [3]float64 `json:"load"`
		NCPU     int        `json:"ncpu"`
	} `json:"gauges"`
	Disks   []diskDoc       `json:"disks"`
	PSI     [3]float64      `json:"psi"`
	TopCPU  []procDoc       `json:"topCpu"`
	TopMem  []procDoc       `json:"topMem"`
	Panel   panelDoc        `json:"panel"`
	Alerts  []alertDoc      `json:"alerts"`
	Docker  map[string]int  `json:"docker"`
	Hog     map[string]bool `json:"hog"`
	Notices []notify.Notice `json:"notices"` // active, in screen order
	Board   boardDoc        `json:"board"`   // which firmware, and whether this daemon drives it
}

type diskDoc struct {
	Name string `json:"name"`
	Pct  int    `json:"pct"`
}

type procDoc struct {
	Name string  `json:"name"`
	V    float64 `json:"v"`
}

type cellDoc struct {
	Slot  int    `json:"slot"`
	Style int    `json:"style"`
	Pct   int    `json:"pct"`
	Text  string `json:"text"`
}

type pageDoc struct {
	Name     string `json:"name"`
	Eligible bool   `json:"eligible"`
	Enabled  bool   `json:"enabled"`
}

type panelDoc struct {
	Page   string    `json:"page"`
	Sub    int       `json:"sub"`
	Subs   int       `json:"subs"`
	Pin    string    `json:"pin"`
	Full   bool      `json:"full"`
	Pages  []pageDoc `json:"pages"`
	Cells  []cellDoc `json:"cells"`
	Accent [2]string `json:"accent"` // notice page: palette 9 and 10, #RRGGBB
}

type alertDoc struct {
	Key    string `json:"key"`
	Title  string `json:"title"`
	Detail string `json:"detail"`
	Sev    int    `json:"sev"`
}

var latest atomic.Pointer[stateDoc]

// allPages is every page the panel knows, in rotation order.
var allPages = []string{"dash", "cpu", "ram", "net", "claude", "docker", "system", "top", "rollcpu", "rollmem"}

func (p *panel) doc(s *snapshot, e *engine, down, up int, uptime uint32, now time.Time) *stateDoc {
	d := &stateDoc{Connected: true, Updated: now, Board: boardState()}
	g := &d.Gauges
	g.CPU, g.RAM, g.Swap, g.CPUT, g.GPU, g.GPUT, g.VRAM = s.cpu, s.ram, s.swap, s.cpuT, s.gpu, s.gpuT, s.vram
	g.GPUW, g.GPULimit, g.NVMeT, g.Latency, g.Down, g.Up, g.Uptime, g.NCPU = s.gpuW, s.gpuLimit, s.nvmeT, s.latency, down, up, uptime, s.ncpu
	for i, l := range s.load {
		g.Load[i] = float64(l) / 100
	}
	d.PSI = s.psi
	if len(s.disks) > 0 { // nil stays "null" in the JSON
		d.Disks = make([]diskDoc, len(s.disks))
	}
	for i, k := range s.disks {
		d.Disks[i] = diskDoc{k.name, k.pct}
	}
	d.TopCPU, d.TopMem = procDocs(s.top.cpu), procDocs(s.top.mem)
	d.Docker = map[string]int{"ok": b2i(s.dock.ok), "total": s.dock.total, "up": s.dock.up, "unhealthy": s.dock.unhealthy, "restarting": s.dock.restarting, "bad": s.dock.bad}
	d.Hog = map[string]bool{"cpu": loadBusy(s), "mem": ramBusy(s)}
	c := cfg()
	d.Panel = panelDoc{Page: p.name, Sub: p.sub, Subs: subPages(p.name, s), Pin: c.Pin, Full: p.full}
	d.Panel.Pages = make([]pageDoc, len(allPages))
	for i, n := range allPages {
		d.Panel.Pages[i] = pageDoc{n, slices.Contains(p.elig, n), !c.Disabled[n]} // p.elig: worked out by this tick
	}
	if len(p.shown) > 0 {
		d.Panel.Cells = make([]cellDoc, 0, len(p.shown))
	}
	for slot, cl := range p.shown {
		d.Panel.Cells = append(d.Panel.Cells, cellDoc{int(slot), int(cl.style), int(cl.pct), cl.text})
	}
	sort.Slice(d.Panel.Cells, func(i, j int) bool { return d.Panel.Cells[i].Slot < d.Panel.Cells[j].Slot })
	if p.name == "note" {
		d.Panel.Accent = [2]string{hex565(p.accent[0]), hex565(p.accent[1])}
	}
	d.Notices, _ = notes.list(now)
	list, _, _ := e.view(now)
	for _, a := range list {
		d.Alerts = append(d.Alerts, alertDoc{a.key, a.title, a.detail, a.sev})
	}
	return d
}

func hex565(c uint16) string {
	r, g, b := c>>11&31, c>>5&63, c&31
	return fmt.Sprintf("#%02X%02X%02X", r<<3|r>>2, g<<2|g>>4, b<<3|b>>2)
}

func procDocs(v []nameVal) []procDoc {
	if len(v) == 0 {
		return nil // "null" in the JSON
	}
	o := make([]procDoc, len(v))
	for i, t := range v {
		o[i] = procDoc{t.name, t.v}
	}
	return o
}

func disconnected() {
	if d := latest.Load(); d != nil && d.Connected {
		c := *d
		c.Connected = false
		latest.Store(&c)
	}
}

// ---- control socket: newline-delimited JSON requests, one JSON reply each ----

func ctlSock() string { return notify.Socket() }

type ctlReq struct {
	Cmd      string          `json:"cmd"`
	Sev      string          `json:"sev"` // notify, short form: sev + ttl + text
	TTL      int             `json:"ttl"`
	Text     string          `json:"text"`
	Message  *notify.Message `json:"message"` // notify, full form
	ID       string          `json:"id"`      // notice, dismiss
	All      bool            `json:"all"`     // dismiss
	By       string          `json:"by"`      // dismiss: who did it (cli, ui)
	Page     string          `json:"page"`
	Settings *settings       `json:"settings"`
}

type ctlResp struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
	Data  any    `json:"data,omitempty"`
}

func handleCtl(r ctlReq, e *engine) ctlResp {
	switch r.Cmd {
	case "state":
		d := latest.Load()
		if d == nil {
			return ctlResp{OK: true, Data: &stateDoc{}}
		}
		return ctlResp{OK: true, Data: d}
	case "settings":
		return ctlResp{OK: true, Data: cfg()}
	case "set":
		if r.Settings == nil {
			return ctlResp{Error: "missing settings"}
		}
		setCfg(*r.Settings)
		return ctlResp{OK: true, Data: cfg()}
	case "pin": // page == "" releases
		c := cfg()
		if r.Page != "" {
			ok := false
			for _, n := range allPages {
				ok = ok || n == r.Page
			}
			if !ok {
				return ctlResp{Error: "unknown page"}
			}
		}
		c.Pin = r.Page
		setCfg(c)
		return ctlResp{OK: true, Data: cfg()}
	case "next":
		skipAt.Store(true)
		return ctlResp{OK: true}
	case "prev":
		prevAt.Store(true)
		return ctlResp{OK: true}
	case "goto":
		ok := r.Page == "settings"
		for _, n := range allPages {
			ok = ok || n == r.Page
		}
		if !ok {
			return ctlResp{Error: "unknown page"}
		}
		queueTouch(touchEv{kind: 'g', page: r.Page})
		return ctlResp{OK: true}
	case "notify":
		m := r.Message
		if m == nil { // short form: {"sev":"warn","ttl":60,"text":"build finished"}
			m = &notify.Message{Level: r.Sev, Title: r.Text}
			if r.TTL > 0 {
				m.TTL = &r.TTL
			}
		}
		n, err := notes.add(*m, time.Now())
		if err != nil {
			return ctlResp{Error: err.Error()}
		}
		return ctlResp{OK: true, Data: n}
	case "notices":
		a, rc := notes.list(time.Now())
		return ctlResp{OK: true, Data: map[string]any{"active": a, "recent": rc}}
	case "notice":
		n, ok := notes.get(r.ID, time.Now())
		if !ok {
			return ctlResp{Error: "unknown id " + strconv.Quote(r.ID)}
		}
		return ctlResp{OK: true, Data: n}
	case "dismiss":
		if r.ID == "" && !r.All {
			return ctlResp{Error: "give an id or all"}
		}
		by := r.By
		if by == "" {
			by = "cli"
		}
		ids := notes.dismiss(r.ID, r.All, by, time.Now())
		if ids == nil {
			if r.All {
				ids = []string{}
			} else if _, ok := notes.get(r.ID, time.Now()); !ok {
				return ctlResp{Error: "unknown id " + strconv.Quote(r.ID)}
			} else {
				ids = []string{} // already closed
			}
		}
		return ctlResp{OK: true, Data: map[string]any{"dismissed": ids}}
	case "version":
		return ctlResp{OK: true, Data: versionInfo()}
	case "update": // look for a release now; a newer one that fits the board is installed and run
		to, err := updateSelf()
		if err != nil {
			return ctlResp{Error: err.Error()}
		}
		if to != "" {
			go func() { time.Sleep(300 * time.Millisecond); restart() }() // after this reply is out
		}
		return ctlResp{OK: true, Data: map[string]any{"updated": to, "status": updateState().Status}}
	}
	return ctlResp{Error: "unknown cmd"}
}

type versionDoc struct {
	Version  string    `json:"version"`
	API      int       `json:"api"`
	Proto    int       `json:"proto"`
	MinProto int       `json:"minProto"`
	Board    boardDoc  `json:"board"`
	Update   updateDoc `json:"update"`
}

func versionInfo() versionDoc {
	return versionDoc{Version: version.Get(), API: version.API, Proto: version.Proto, MinProto: version.MinProto, Board: boardState(), Update: updateState()}
}

// listenCtl serves the control socket, private to this user. With shared, it also serves the notice commands to
// every user of the machine on an abstract socket (Linux: no file, so no permissions; it goes away with the
// process), so tft-notify works from any account.
func listenCtl(e *engine, shared bool) {
	path := ctlSock()
	os.Remove(path)
	if l, err := net.Listen("unix", path); err == nil {
		os.Chmod(path, 0o600)
		serveCtl(l, e, nil)
	}
	if shared {
		if l, err := net.Listen("unix", notify.SharedSocket); err == nil {
			serveCtl(l, e, sharedCmds)
		}
	}
}

// sharedCmds is what other users may do: show, read and dismiss notices, and ask which versions are running.
var sharedCmds = map[string]bool{"notify": true, "notices": true, "notice": true, "dismiss": true, "version": true}

func serveCtl(l net.Listener, e *engine, only map[string]bool) {
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				sc := bufio.NewScanner(c)
				sc.Buffer(make([]byte, 64<<10), 64<<10)
				enc := json.NewEncoder(c)
				for sc.Scan() {
					var r ctlReq
					if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
						enc.Encode(ctlResp{Error: "bad json"})
						continue
					}
					if only != nil && !only[r.Cmd] {
						enc.Encode(ctlResp{Error: "not allowed from another user: " + strconv.Quote(r.Cmd)})
						continue
					}
					enc.Encode(handleCtl(r, e))
				}
			}(c)
		}
	}()
}
