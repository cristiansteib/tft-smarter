package main

import (
	"fmt"
	"sort"
	"time"
)

// panel drives the bottom info band: it rotates through pages, lets alerts take over,
// diffs the cells against what the Mega already shows and paces what it sends.
type panel struct {
	name      string
	sub       int
	enteredAt time.Time
	shown     map[byte]cell
	pending   [][]byte
	lastFull  time.Time
	lastBand  [4]int
	lastBandT time.Time
	manual    string // page chosen with "Next" in the UI
	manualEnd time.Time
	full      bool      // the Mega currently shows a full-screen page
	hold      time.Time // the Mega is busy repainting after a screen switch: send nothing until then
}

func newPanel() *panel { return &panel{shown: map[byte]cell{}} }

func pal(v, warn, crit int) byte {
	switch {
	case v >= crit:
		return pRed
	case v >= warn:
		return pYellow
	}
	return pGreen
}

func gb(b float64) string {
	if b >= 1<<30 {
		return fmt.Sprintf("%.1fG", b/(1<<30))
	}
	return fmt.Sprintf("%.0fM", b/(1<<20))
}

func wh(v float64) string {
	if v >= 1000 {
		return fmt.Sprintf("%.2fkWh", v/1000)
	}
	return fmt.Sprintf("%.0fWh", v)
}

func short(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// Rolling credits: the top processes scroll up through the two rows of the band, one row per step.
// They only join the rotation while the machine is busy (load per core / RAM).

func isRoll(name string) bool { return name == "rollcpu" || name == "rollmem" }

// A process is a hog when it eats at least one good chunk of a core / of the RAM.
func hogsCPU(s *snapshot) []nameVal {
	var o []nameVal
	for _, t := range s.top.cpu {
		if t.v >= cfg().HogCPU {
			o = append(o, t)
		}
	}
	return o
}

func hogsMem(s *snapshot) []nameVal {
	var o []nameVal
	for _, t := range s.top.mem {
		if s.memTotal > 0 && t.v/s.memTotal*100 >= cfg().HogMem {
			o = append(o, t)
		}
	}
	return o
}

// the credits only run when the machine is busy AND somebody is to blame
func loadBusy(s *snapshot) bool {
	return (s.ncpu > 0 && float64(s.load[0])/float64(s.ncpu) >= cfg().LoadTrigger) && len(hogsCPU(s)) > 0
}
func ramBusy(s *snapshot) bool { return (s.ram >= cfg().RamTrigger) && len(hogsMem(s)) > 0 }

// creditLines returns the title and one row per hog (the rows roll when there are more than fit).
func creditLines(name string, s *snapshot) (cell, []cell) {
	var rows []cell
	if name == "rollcpu" {
		for _, t := range hogsCPU(s) {
			rows = append(rows, cell{0, pal(int(t.v), 50, 150) | sBar, byte(min(int(t.v), 100)), fmt.Sprintf("%s\t%.0f%%", short(t.name, 13), t.v)})
		}
		return cell{0, pCyan | sBig, 0, fmt.Sprintf("Hogging CPU\tload %.1f", float64(s.load[0])/100)}, rows
	}
	for _, t := range hogsMem(s) {
		rows = append(rows, cell{0, pMagenta | sBar, byte(min(t.v/s.memTotal*100*3, 100)), fmt.Sprintf("%s\t%s", short(t.name, 13), gb(t.v))})
	}
	return cell{0, pCyan | sBig, 0, fmt.Sprintf("Hogging RAM\t%d%% used", s.ram)}, rows
}

// Pages: "dash" (the gauges screen, band shows the disks) and "alerts" (band takeover) live on the dashboard;
// every other page replaces the whole screen.
// rollRows is how many hog rows fit: the CPU page keeps the lower part of the screen for the scope.
func rollRows(name string) int {
	if name == "rollcpu" {
		return 4
	}
	return 9
}

// screenMode is the byte for frameScreen: 0 dashboard, 1/2 full page (2 clears first), +0x10 draws the load scope.
func screenMode(name string, clear bool) byte {
	if !isFull(name) {
		return 0
	}
	m := byte(1)
	if clear {
		m = 2
	}
	if name == "rollcpu" {
		m |= 0x10
	}
	return m
}

func isFull(name string) bool { return name != "dash" && name != "alerts" && name != "" }

func subPages(name string, s *snapshot) int {
	if isRoll(name) {
		_, rows := creditLines(name, s)
		return max(1, len(rows)-rollRows(name)+1) // rolls only when there are more hogs than rows
	}
	return 1
}

func dwell(name string, sub int) time.Duration {
	if isRoll(name) {
		return time.Duration(cfg().CreditStepMs) * time.Millisecond
	}
	if name == "dash" {
		return 3 * time.Duration(cfg().DwellSec) * time.Second // the dashboard is home: it stays longest
	}
	return time.Duration(cfg().DwellSec) * time.Second
}

// groupNames is the rotation: eligible pages minus the ones the user disabled in the UI.
func groupNames(s *snapshot, alerts bool) []string {
	all := eligibleNames(s, alerts)
	dis := cfg().Disabled
	var n []string
	for _, x := range all {
		if !dis[x] {
			n = append(n, x)
		}
	}
	if len(n) == 0 {
		return all // never leave the band without a page
	}
	return n
}

func eligibleNames(s *snapshot, alerts bool) []string {
	var n []string
	if alerts {
		n = append(n, "alerts")
	}
	if loadBusy(s) || ramBusy(s) { // busy machine: the credits take over the band
		if loadBusy(s) {
			n = append(n, "rollcpu")
		}
		if ramBusy(s) {
			n = append(n, "rollmem")
		}
		return n
	}
	n = append(n, "dash")
	if s.dock.ok && s.dock.unhealthy+s.dock.bad+s.dock.restarting > 0 { // healthy docker is just noise
		n = append(n, "docker")
	}
	if systemWorth(s) {
		n = append(n, "system")
	}
	if len(s.top.cpu) > 0 && s.top.cpu[0].v >= 10 {
		n = append(n, "top")
	}
	return n
}

// systemWorth: the health page is only worth a slot when something there is off.
func systemWorth(s *snapshot) bool {
	return s.psi[0] >= 5 || s.psi[1] >= 2 || s.psi[2] >= 10 || s.nvmeT >= 60 || s.latency < 0 || s.latency >= 100
}

func (p *panel) build(name string, sub int, s *snapshot, list []*alert, take bool, since, now time.Time) []cell {
	var cs []cell
	if isFull(name) {
		return p.buildFull(name, sub, s)
	}
	switch name {
	case "dash": // gauges stay up top; the band shows the four fullest disks, fullest first
		ds := append([]disk(nil), s.disks...)
		sort.Slice(ds, func(i, j int) bool { return ds[i].pct > ds[j].pct })
		for i, d := range ds[:min(4, len(ds))] {
			cs = append(cs, cell{byte(i), pal(d.pct, 70, 90) | sBar, byte(d.pct), fmt.Sprintf("%s\t%d%%", short(d.name, 6), d.pct)})
		}
	case "alerts":
		if len(list) == 0 {
			break
		}
		step := int(now.Unix() / 4)
		if take {
			step = int(now.Sub(since) / (4 * time.Second))
		}
		a := list[step%len(list)]
		col := map[int]byte{sevOK: pGreen, sevInfo: pCyan, sevWarn: pOrange, sevCrit: pRed}[a.sev]
		cs = append(cs, cell{0, col | sWide | sBig | sBright, 0, short(a.title, 24)})
		d := short(a.detail, 30)
		if len(list) > 1 {
			d = fmt.Sprintf("%s  %d/%d", short(a.detail, 24), step%len(list)+1, len(list))
		}
		cs = append(cs, cell{2, pWhite | sWide, 0, d})
	}
	return cs
}

// tick returns the frames to send now. It must be called every ~250 ms.
func (p *panel) tick(now time.Time, s *snapshot, e *engine) [][]byte {
	list, take, since := e.view(now)
	real := 0
	maxSev := 0
	for _, a := range list {
		if a.sev > sevOK {
			real++
		}
		if take || a.sev > sevOK {
			maxSev = max(maxSev, a.sev)
		}
	}
	names := groupNames(s, take && len(list) > 0 || real > 0)
	var name string
	sub := 0
	skip := skipAt.Swap(false) // "next page" from the UI
	if skip && cfg().Pin == "" {
		// Next = the next enabled page after the current one, held for 30 s even over credits or alerts
		dis, at := cfg().Disabled, -1
		for i, n := range allPages {
			if n == p.name {
				at = i
			}
		}
		for k := 1; k <= len(allPages); k++ {
			if n := allPages[(at+k+len(allPages))%len(allPages)]; !dis[n] && !(n == "rollcpu" && len(hogsCPU(s)) == 0) && !(n == "rollmem" && len(hogsMem(s)) == 0) {
				p.manual, p.manualEnd = n, now.Add(30*time.Second)
				break
			}
		}
		skip = false
	}
	pin := cfg().Pin
	override := false
	if pin == "" && p.manual != "" && now.Before(p.manualEnd) {
		pin, override = p.manual, true
	}
	if pin != "" && (override || !(take && len(list) > 0)) {
		name = pin // pinned from the UI: hold this page (its own screens still roll); alerts still win
		if p.name == pin {
			sub = p.sub
			if skip || now.Sub(p.enteredAt) >= dwell(name, sub) {
				p.enteredAt = now
				sub = (sub + 1) % subPages(name, s)
			}
		} else {
			p.enteredAt = now
		}
	} else if take && len(list) > 0 {
		name = "alerts"
	} else {
		cur := -1
		for i, n := range names {
			if n == p.name {
				cur = i
			}
		}
		if cur < 0 {
			name, p.enteredAt = names[0], now
		} else {
			name, sub = p.name, p.sub
			if skip || now.Sub(p.enteredAt) >= dwell(name, sub) {
				p.enteredAt = now
				if sub+1 < subPages(name, s) {
					sub++
				} else {
					name, sub = names[(cur+1)%len(names)], 0
				}
			}
		}
	}
	var out [][]byte
	changed := name != p.name || sub != p.sub
	if changed && isRoll(name) && name == p.name { // rolling: just diff the two rows, no full clear
		p.sub, changed = sub, false
	}
	if changed {
		p.name, p.sub = name, sub
		if name == "alerts" && take {
			p.enteredAt = now
		}
		p.shown = map[byte]cell{}
		p.full = isFull(name)
		p.pending = [][]byte{screenFrame(screenMode(name, true))} // full pages clear the screen on entry
		if !p.full {
			p.pending = append(p.pending, bandFrame(0, 0, maxSev, true, true))
		}
		p.lastFull = now
	} else if now.Sub(p.lastFull) >= 5*time.Second {
		p.shown = map[byte]cell{}
		p.pending = append([][]byte{screenFrame(screenMode(p.name, false))}, p.pending...) // self-heal if the Mega missed the switch
		p.lastFull = now
	}
	idx := 0
	for i, n := range names {
		if n == name {
			idx = i
		}
	}
	border := maxSev < sevCrit || now.Second()%2 == 0
	key := [4]int{idx, len(names), maxSev, b2i(border)}
	if !p.full && (key != p.lastBand || now.Sub(p.lastBandT) >= 5*time.Second || changed) {
		p.lastBand, p.lastBandT = key, now
		p.pending = append(p.pending, bandFrame(idx, len(names), maxSev, false, border)) // queued, so it lands after a screen switch
	}
	want := p.build(name, sub, s, list, take, since, now)
	have := map[byte]bool{}
	for _, c := range want {
		have[c.slot] = true
		if old, ok := p.shown[c.slot]; !ok || old != c {
			p.shown[c.slot] = c
			p.pending = append(p.pending, p.paint(c))
		}
	}
	for slot := range p.shown { // blank cells that are no longer used
		if !have[slot] {
			delete(p.shown, slot)
			p.pending = append(p.pending, p.paint(cell{slot: slot}))
		}
	}
	if now.Before(p.hold) {
		return out
	}
	n := min(3, len(p.pending)) // pacing: the Mega draws slowly and has a small RX buffer
	if n > 0 && p.pending[0][2] == frameScreen {
		n = 1 // clearing / repainting the screen takes a while: let it finish before sending rows
		p.hold = now.Add(700 * time.Millisecond)
	}
	out = append(out, p.pending[:n]...)
	p.pending = p.pending[n:]
	return out
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// buildFull lays out a full-screen page: row 0 is the title, rows 1..9 the content.
func (p *panel) buildFull(name string, sub int, s *snapshot) []cell {
	var cs []cell
	row := func(i int, style byte, pct int, label, val string) {
		cs = append(cs, cell{byte(i), style, byte(min(max(pct, 0), 100)), label + "\t" + val})
	}
	switch name {
	case "rollcpu", "rollmem":
		t, rows := creditLines(name, s)
		cs = append(cs, t)
		win := rollRows(name)
		start := 0
		if len(rows) > win {
			start = min(sub, len(rows)-win)
		}
		for r := 0; r < win && start+r < len(rows); r++ {
			c := rows[start+r]
			c.slot = byte(r + 1)
			cs = append(cs, c)
		}
		if name == "rollcpu" { // the scope owns the middle; RAM sits in the last row
			row(9, pal(s.ram, 60, 85)|sBar|sSwap, s.ram, fmt.Sprintf("%03d", min(s.swap, 100))+"RAM", fmt.Sprintf("%d%%", s.ram))
		}
	case "top":
		cs = append(cs, cell{0, pCyan | sBig, 0, "Busiest\tcpu and ram"})
		for i, t := range s.top.cpu[:min(5, len(s.top.cpu))] {
			row(i+1, pal(int(t.v), 50, 150)|sBar, int(t.v), short(t.name, 13), fmt.Sprintf("%.0f%%", t.v))
		}
		for i, t := range s.top.mem[:min(4, len(s.top.mem))] {
			row(6+i, pMagenta|sBar, int(t.v/s.memTotal*100*3), short(t.name, 13), gb(t.v))
		}
	case "system":
		cs = append(cs, cell{0, pCyan | sBig, 0, "System\thealth"})
		for i, n := range []string{"CPU stalls", "Memory stalls", "IO stalls"} {
			v := int(s.psi[i] + 0.5)
			row(i+1, pal(v, 20, 50)|sBar, v, n, fmt.Sprintf("%d%%", v))
		}
		nv := byte(pSky)
		if s.nvmeT >= 80 {
			nv = pRed
		} else if s.nvmeT >= 70 {
			nv = pOrange
		}
		row(4, nv|sBar, s.nvmeT, "NVMe", fmt.Sprintf("%dC", s.nvmeT))
		if s.latency < 0 {
			row(5, pRed|sBar, 0, "Ping", "failed")
		} else {
			row(5, pal(s.latency, 50, 150)|sBar, s.latency, "Ping", fmt.Sprintf("%dms", s.latency))
		}
		row(6, pCyan|sBar, s.gpuW*100/max(s.gpuLimit, 1), "GPU power", fmt.Sprintf("%dW", s.gpuW))
		row(7, pal(s.swap, 25, 60)|sBar, s.swap, "Swap", fmt.Sprintf("%d%%", s.swap))
	case "docker":
		d := s.dock
		bad := d.unhealthy + d.bad + d.restarting
		col := byte(pGreen)
		if bad > 0 {
			col = pOrange
		}
		cs = append(cs, cell{0, col | sBig, 0, fmt.Sprintf("Docker\t%d/%d up", d.up, d.total)})
		r := 1
		for _, n := range d.badNames[:min(4, len(d.badNames))] {
			row(r, pRed|sBright, 0, short(n, 22), "failing")
			r++
		}
		for _, t := range d.top {
			if r > 9 {
				break
			}
			pct := 100.0
			if d.top[0].v > 0 {
				pct = t.v / d.top[0].v * 100
			}
			row(r, pSky|sBar, int(pct), short(t.name, 13), gb(t.v))
			r++
		}
	}
	return cs
}

// paint encodes a cell for the screen the Mega is showing (a band cell or a full-screen row).
func (p *panel) paint(c cell) []byte {
	if p.full {
		return lineFrame(c)
	}
	return cellFrame(c)
}
