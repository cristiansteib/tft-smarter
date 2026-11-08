package main

import (
	"fmt"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	sevOK   = 0 // transient "resolved" message
	sevInfo = 1
	sevWarn = 2
	sevCrit = 3
)

type alert struct {
	key, title, detail string
	sev                int
	since, expire      time.Time
}

type snapshot struct {
	cpu, ram, swap, cpuT, gpu, gpuT, vram int
	load                                  [3]int
	ncpu                                  int
	disks                                 []disk
	cl                                    claudeInfo
	gpuW, gpuLimit, nvmeT                 int
	pcW                                   int
	whBoot                                float64
	psi                                   [3]float64
	fullMem, fullIO                       float64
	latency, latFails                     int
	dock                                  dockerInfo
	top                                   topInfo
	memTotal                              float64 // bytes
}

// engine evaluates alert rules with hysteresis and decides when alerts take over the info panel.
type engine struct {
	mu         sync.Mutex
	lvl        map[string]int // latched level per key
	count      map[string]int // consecutive true ticks per key
	active     map[string]*alert
	notes      map[string]*alert
	resolved   []*alert
	announced  map[string]int
	takeStart  time.Time
	takeUntil  time.Time
	lastTake   time.Time
	noteSeq    int
	lastActive int
}

func newEngine() *engine {
	return &engine{lvl: map[string]int{}, count: map[string]int{}, active: map[string]*alert{},
		notes: map[string]*alert{}, announced: map[string]int{}}
}

func (e *engine) latch(key string, v, warn, warnClear, crit, critClear float64) int {
	cur, n := e.lvl[key], 0
	if v >= warn || (cur >= sevWarn && v >= warnClear) {
		n = sevWarn
	}
	if v >= crit || (cur == sevCrit && v >= critClear) {
		n = sevCrit
	}
	e.lvl[key] = n
	return n
}

// sustained is true once cond has held for secs consecutive evaluations.
func (e *engine) sustained(key string, cond bool, secs int) bool {
	if !cond {
		e.count[key] = 0
		return false
	}
	e.count[key]++
	return e.count[key] >= secs
}

// notify adds a user notification (from the notify socket).
func (e *engine) notify(sev int, ttl time.Duration, text string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.noteSeq++
	key := "notify:" + strconv.Itoa(e.noteSeq)
	now := time.Now()
	e.notes[key] = &alert{key: key, sev: sev, title: text, detail: "notification", since: now, expire: now.Add(ttl)}
}

// eval is called once per second.
func (e *engine) eval(s *snapshot, now time.Time) {
	e.mu.Lock()
	defer e.mu.Unlock()
	want := map[string]*alert{}
	add := func(key string, sev int, title, detail string) {
		if sev > sevOK {
			want[key] = &alert{key: key, sev: sev, title: title, detail: detail}
		}
	}
	for _, d := range s.disks {
		k := "disk:" + d.name
		add(k, e.latch(k, float64(d.pct), 90, 88, 95, 93), fmt.Sprintf("DISK %s %d%% FULL", d.name, d.pct), "Free up space on "+d.name)
	}
	if e.sustained("cpuT", s.cpuT >= 85, 10) || e.lvl["cpuT"] > 0 {
		add("cpuT", e.latch("cpuT", float64(s.cpuT), 85, 82, 90, 87), fmt.Sprintf("CPU HOT %dC", s.cpuT), "CPU temperature is high")
	}
	add("gpuT", e.latch("gpuT", float64(s.gpuT), 83, 80, 88, 85), fmt.Sprintf("GPU HOT %dC", s.gpuT), "GPU temperature is high")
	add("nvme", e.latch("nvme", float64(s.nvmeT), 75, 72, 84, 81), fmt.Sprintf("NVME HOT %dC", s.nvmeT), "NVMe temperature is high")
	add("ram", e.latch("ram", float64(s.ram), 92, 88, 97, 94), fmt.Sprintf("RAM %d%% USED", s.ram), "Memory is almost exhausted")
	add("swap", e.latch("swap", float64(s.swap), 50, 45, 101, 101), fmt.Sprintf("SWAP %d%% USED", s.swap), "System is swapping")
	per5 := float64(s.load[1]) / 100 / float64(max(s.ncpu, 1))
	if e.sustained("load", per5 >= 1.5, 120) || e.lvl["load"] > 0 {
		add("load", e.latch("load", per5, 1.5, 1.2, 99, 99), fmt.Sprintf("LOAD HIGH %.1f/core", per5), "5 minute load per core")
	}
	add("psimem", e.latch("psimem", s.fullMem, 101, 101, 5, 2), "MEMORY PRESSURE", fmt.Sprintf("tasks stalled on memory (full %.0f%%)", s.fullMem))
	add("psiio", e.latch("psiio", s.fullIO, 20, 12, 101, 101), "DISK IO STALLS", fmt.Sprintf("tasks stalled on IO (full %.0f%%)", s.fullIO))
	if s.dock.ok {
		if s.dock.restarting > 0 {
			add("dock:restart", sevCrit, "CONTAINER RESTARTING", strings.Join(s.dock.badNames, ", "))
		}
		if s.dock.unhealthy > 0 {
			sev := sevWarn
			if e.sustained("dock:unh", true, 300) {
				sev = sevCrit
			}
			add("dock:unhealthy", sev, fmt.Sprintf("%d CONTAINER UNHEALTHY", s.dock.unhealthy), strings.Join(s.dock.badNames, ", "))
		} else {
			e.count["dock:unh"] = 0
		}
		if s.dock.bad > 0 {
			add("dock:exited", sevWarn, fmt.Sprintf("%d CONTAINER EXITED", s.dock.bad), strings.Join(s.dock.badNames, ", "))
		}
	}
	if s.latFails >= 3 {
		add("net", sevWarn, "NO INTERNET", "3 probes to 1.1.1.1 failed")
	}
	for k, n := range e.notes {
		if now.After(n.expire) {
			delete(e.notes, k)
			continue
		}
		want[k] = n
	}
	// diff against the previous evaluation
	for k, a := range want {
		if old, ok := e.active[k]; ok {
			a.since = old.since
		} else if a.since.IsZero() {
			a.since = now
		}
		if prev, ok := e.announced[k]; !ok || a.sev > prev {
			e.announced[k] = a.sev
			e.takeStart, e.takeUntil, e.lastTake = now, now.Add(20*time.Second), now
		}
	}
	for k, old := range e.active {
		if _, ok := want[k]; !ok {
			delete(e.announced, k)
			if !strings.HasPrefix(k, "notify:") {
				e.resolved = append(e.resolved, &alert{key: k, sev: sevOK, title: "RESOLVED", detail: old.title, expire: now.Add(4 * time.Second)})
				if now.After(e.takeUntil) {
					e.takeStart, e.takeUntil = now, now.Add(4*time.Second)
				}
			}
		}
	}
	e.active = want
	// periodic reminder so a long-lived alert (e.g. a 91% disk) does not hog the panel
	if len(want) > 0 && !now.Before(e.takeUntil) {
		every := 15 * time.Minute
		for _, a := range want {
			if a.sev == sevCrit {
				every = 2 * time.Minute
			}
		}
		if now.Sub(e.lastTake) >= every {
			e.takeStart, e.takeUntil, e.lastTake = now, now.Add(20*time.Second), now
		}
	}
}

// view returns the alerts to display (most severe first) and whether the panel is in takeover.
func (e *engine) view(now time.Time) (list []*alert, takeover bool, since time.Time) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, a := range e.active {
		list = append(list, a)
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].sev != list[j].sev {
			return list[i].sev > list[j].sev
		}
		return list[i].key < list[j].key
	})
	takeover = now.Before(e.takeUntil)
	if takeover {
		keep := e.resolved[:0]
		for _, r := range e.resolved {
			if now.Before(r.expire) {
				keep = append(keep, r)
				list = append(list, r)
			}
		}
		e.resolved = keep
	}
	return list, takeover, e.takeStart
}

// ---- notify socket: `tft-monitor notify [-sev warn] [-ttl 60] text` ----

func notifySock() string {
	d := os.Getenv("XDG_RUNTIME_DIR")
	if d == "" {
		d = "/run/user/" + strconv.Itoa(os.Getuid())
	}
	return d + "/tft-monitor.sock"
}

func listenNotify(e *engine) {
	path := notifySock()
	os.Remove(path)
	c, err := net.ListenPacket("unixgram", path)
	if err != nil {
		return
	}
	go func() {
		buf := make([]byte, 512)
		for {
			n, _, err := c.ReadFrom(buf)
			if err != nil {
				return
			}
			f := strings.SplitN(string(buf[:n]), "\x1f", 3)
			if len(f) != 3 {
				continue
			}
			sev := map[string]int{"info": sevInfo, "warn": sevWarn, "crit": sevCrit}[f[0]]
			ttl, _ := strconv.Atoi(f[1])
			e.notify(max(sev, sevInfo), time.Duration(max(ttl, 5))*time.Second, f[2])
		}
	}()
}

func sendNotify(sev string, ttl int, text string) error {
	c, err := net.Dial("unixgram", notifySock())
	if err != nil {
		return fmt.Errorf("is tft-monitor running? %w", err)
	}
	defer c.Close()
	_, err = c.Write([]byte(sev + "\x1f" + strconv.Itoa(ttl) + "\x1f" + text))
	return err
}
