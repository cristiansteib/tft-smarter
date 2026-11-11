package main

import (
	"bufio"
	"encoding/json"
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
	Disks  []diskDoc       `json:"disks"`
	PSI    [3]float64      `json:"psi"`
	TopCPU []procDoc       `json:"topCpu"`
	TopMem []procDoc       `json:"topMem"`
	Panel  panelDoc        `json:"panel"`
	Alerts []alertDoc      `json:"alerts"`
	Docker map[string]int  `json:"docker"`
	Hog    map[string]bool `json:"hog"`
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
	Page  string    `json:"page"`
	Sub   int       `json:"sub"`
	Subs  int       `json:"subs"`
	Pin   string    `json:"pin"`
	Full  bool      `json:"full"`
	Pages []pageDoc `json:"pages"`
	Cells []cellDoc `json:"cells"`
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
	d := &stateDoc{Connected: true, Updated: now}
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
	list, _, _ := e.view(now)
	for _, a := range list {
		d.Alerts = append(d.Alerts, alertDoc{a.key, a.title, a.detail, a.sev})
	}
	return d
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

func ctlSock() string {
	d := os.Getenv("XDG_RUNTIME_DIR")
	if d == "" {
		d = "/run/user/" + strconv.Itoa(os.Getuid())
	}
	return d + "/tft-monitor.ctl"
}

type ctlReq struct {
	Cmd      string    `json:"cmd"`
	Sev      string    `json:"sev"`
	TTL      int       `json:"ttl"`
	Text     string    `json:"text"`
	Page     string    `json:"page"`
	Settings *settings `json:"settings"`
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
		sev := map[string]int{"info": sevInfo, "warn": sevWarn, "crit": sevCrit}[r.Sev]
		if r.Text == "" {
			return ctlResp{Error: "empty text"}
		}
		e.notify(max(sev, sevInfo), time.Duration(max(r.TTL, 5))*time.Second, short(r.Text, 60))
		return ctlResp{OK: true}
	}
	return ctlResp{Error: "unknown cmd"}
}

func listenCtl(e *engine) {
	path := ctlSock()
	os.Remove(path)
	l, err := net.Listen("unix", path)
	if err != nil {
		return
	}
	os.Chmod(path, 0o600)
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
					enc.Encode(handleCtl(r, e))
				}
			}(c)
		}
	}()
}
