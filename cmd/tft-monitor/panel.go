package main

import (
	"fmt"
	"sort"
	"strings"
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
	repaint   bool      // force the board to repaint the current screen (the menu was drawn over it)
	resend    bool      // the board repainted itself: send every cell again
	lastView  string    // last real page shown, where Settings returns to
	lastTap   touchEv   // the previous tap, for double-tap detection
	sentSlot  int       // last menu slot sent to the board (-1 = none yet)
}

func newPanel() *panel { return &panel{shown: map[byte]cell{}, sentSlot: -1} }

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
		m |= 0x10 // load scope under the list
	}
	if name == "claude" {
		m |= 0x20 // the logo
	}
	if name == "net" {
		m |= 0x08 // the traffic scope
	}
	return m
}

func isFull(name string) bool { return name != "dash" && name != "alerts" && name != "" }

func subPages(name string, s *snapshot) int {
	if name == "settings" {
		return settingsSubs
	}
	if isRoll(name) {
		_, rows := creditLines(name, s)
		return max(1, len(rows)-rollRows(name)+1) // rolls only when there are more hogs than rows
	}
	return 1
}

const minFull = 10 * time.Second // a full-screen page is never on for less than this

func dwell(name string, sub, subs int) time.Duration {
	if name == "settings" {
		return 24 * time.Hour // its sub-pages change by touch only
	}
	d := time.Duration(cfg().DwellSec) * time.Second
	if isRoll(name) {
		if subs > 1 && sub < subs-1 {
			if sub == 0 {
				return 3 * time.Second // let the list be read before it starts rolling
			}
			return time.Duration(cfg().CreditStepMs) * time.Millisecond
		}
		return max(d, minFull) // the last (or only) screen of the list stays at least 10 s
	}
	if name == "dash" {
		return 3 * d // the dashboard is home: it stays longest
	}
	if isFull(name) {
		return max(d, minFull)
	}
	return d
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

// settle is a hysteresis latch: cond must hold for `rise` before the page joins the rotation and must be
// gone for `fall` before it leaves, so a load that flickers around a threshold does not flip the screen.
type latch struct {
	on    bool
	since time.Time
}

var debounce = map[string]*latch{}

func settle(name string, cond bool, rise, fall time.Duration) bool {
	l := debounce[name]
	if l == nil {
		l = &latch{}
		debounce[name] = l
	}
	now := time.Now()
	if cond == l.on {
		l.since = time.Time{}
		return l.on
	}
	if l.since.IsZero() {
		l.since = now
	}
	d := rise
	if l.on {
		d = fall
	}
	if now.Sub(l.since) >= d {
		l.on, l.since = cond, time.Time{}
	}
	return l.on
}

func eligibleNames(s *snapshot, alerts bool) []string {
	var n []string
	if alerts {
		n = append(n, "alerts")
	}
	busyCPU := settle("rollcpu", loadBusy(s), 8*time.Second, 12*time.Second)
	busyMem := settle("rollmem", ramBusy(s), 8*time.Second, 12*time.Second)
	if busyCPU || busyMem { // busy machine: the credits take over the band
		if busyCPU {
			n = append(n, "rollcpu")
		}
		if busyMem {
			n = append(n, "rollmem")
		}
		return n
	}
	n = append(n, "dash") // home; every other page opens on demand and goes back home on its own
	return n
}

// systemWorth: the health page is only worth a slot when something there is off.
func systemWorth(s *snapshot) bool {
	return s.psi[0] >= 5 || s.psi[1] >= 2 || s.psi[2] >= 10 || s.nvmeT >= 60 || s.latency < 0 || s.latency >= 100
}

func (p *panel) build(name string, sub int, s *snapshot, list []*alert, take bool, since, now time.Time) []cell {
	var cs []cell
	if name == "settings" {
		return p.buildSettings(sub)
	}
	if isFull(name) {
		cs := p.buildFull(name, sub, s)
		if cfg().Pin == name && len(cs) > 0 && cs[0].slot == 0 { // show that the page is held
			t, v, _ := strings.Cut(cs[0].text, "\t")
			cs[0].text = t + "\tpin " + v
		}
		return cs
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
	if skip {
		p.step(1, s, now) // with a page pinned this moves the pin
		skip = false
	}
	if prevAt.Swap(false) {
		p.step(-1, s, now)
	}
	for done := false; !done; { // touch gestures from the board
		select {
		case ev := <-touchCh:
			p.handleTouch(ev, s, now)
		default:
			done = true
		}
	}
	pin := cfg().Pin
	override := false
	if p.manual != "" && now.Before(p.manualEnd) { // a page chosen by touch (or the settings) wins over the pin for a while
		pin, override = p.manual, true
	}
	if pin != "" && (override || isFull(pin) || !(take && len(list) > 0)) { // a pinned full page stays even when an alert fires
		name = pin // pinned from the UI: hold this page (its own screens still roll); alerts still win
		if p.name == pin {
			sub = p.sub
			if skip || now.Sub(p.enteredAt) >= dwell(name, sub, subPages(name, s)) {
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
		if cur < 0 && isFull(p.name) && now.Sub(p.enteredAt) < 10*time.Second {
			name, sub = p.name, p.sub // a full page stays at least 10 s even if its reason just went away
		} else if cur < 0 {
			name, p.enteredAt = names[0], now
		} else {
			name, sub = p.name, p.sub
			if skip || now.Sub(p.enteredAt) >= dwell(name, sub, subPages(name, s)) {
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
	if changed && (isRoll(name) || name == "settings") && name == p.name { // rolling / settings sub-pages: just diff the rows, no full clear
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
	} else if now.Sub(p.lastFull) >= 10*time.Second {
		p.shown = map[byte]cell{}
		p.pending = append([][]byte{screenFrame(screenMode(p.name, false)), pageFrame(menuSlot(p.name), cfg().Pin == p.name)}, p.pending...) // self-heal if the Mega missed the switch
		p.lastFull = now
	}
	if name != "settings" && name != "alerts" && name != "" {
		p.lastView = name
	}
	if p.repaint { // the menu was drawn over this screen: repaint it fully, then every cell again
		p.repaint = false
		p.shown = map[byte]cell{}
		p.pending = [][]byte{screenFrame(screenMode(name, true) | 0x40)}
		if !p.full {
			p.pending = append(p.pending, bandFrame(0, 0, maxSev, true, true))
		}
		p.lastBand, p.lastFull = [4]int{-1}, now
	}
	if p.resend { // the board repainted itself (menu closed / calibrated)
		p.resend = false
		p.shown, p.pending = map[byte]cell{}, nil
		p.lastBand, p.lastFull = [4]int{-1}, now
	}
	pinned := cfg().Pin == name && name != ""
	if slot := int(menuSlot(name)) | b2i(pinned)<<8; slot != p.sentSlot {
		p.sentSlot = slot
		p.pending = append(p.pending, pageFrame(menuSlot(name), pinned))
	}
	idx := 0
	for i, n := range names {
		if n == name {
			idx = i
		}
	}
	border := maxSev < sevCrit || now.Second()%2 == 0
	key := [4]int{idx, len(names), maxSev, b2i(border)}
	if !p.full && (key != p.lastBand || now.Sub(p.lastBandT) >= 10*time.Second || changed) {
		p.lastBand, p.lastBandT = key, now
		p.pending = append(p.pending, bandFrame(idx, len(names), maxSev, false, border)) // queued, so it lands after a screen switch
	}
	want := p.build(name, sub, s, list, take, since, now)
	have := map[byte]bool{}
	for _, c := range want {
		have[c.slot] = true
		if old, ok := p.shown[c.slot]; !ok || !similar(old, c) {
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
			row(8, pal(s.ram, 60, 85)|sBar|sSwap, s.ram, fmt.Sprintf("%03d", min(s.swap, 100))+"RAM", fmt.Sprintf("%d%%", s.ram))
		}
	case "cpu":
		cs = append(cs, cell{0, pCyan | sBig, 0, fmt.Sprintf("CPU\t%d%%", s.cpu)})
		row(1, pal(s.cpu, 60, 85)|sBar, s.cpu, "Usage", fmt.Sprintf("%d%%", s.cpu))
		tc := byte(pSky)
		if s.cpuT >= 80 {
			tc = pRed
		} else if s.cpuT >= 60 {
			tc = pOrange
		}
		row(2, tc|sBar, (s.cpuT-20)*100/80, "Temperature", fmt.Sprintf("%dC", s.cpuT))
		row(3, pWhite, 0, "Load 1 / 5 / 15", fmt.Sprintf("%.1f / %.1f / %.1f", float64(s.load[0])/100, float64(s.load[1])/100, float64(s.load[2])/100))
		per := s.load[0] / max(s.ncpu, 1)
		row(4, pal(per, 60, 100)|sBar, per, "Load per core", fmt.Sprintf("%d%%", per))
		for i, t := range s.top.cpu[:min(3, len(s.top.cpu))] {
			row(5+i, pal(int(t.v), 50, 150)|sBar, int(t.v), short(t.name, 13), fmt.Sprintf("%.0f%%", t.v))
		}
		row(8, pGrey, 0, "Threads", fmt.Sprintf("%d", s.ncpu))
	case "ram":
		cs = append(cs, cell{0, pCyan | sBig, 0, fmt.Sprintf("Memory\t%d%% used", s.ram)})
		row(1, pal(s.ram, 60, 85)|sBar, s.ram, "RAM of "+gb(s.memTotal), gb(s.ramUsed))
		sw := 0
		if s.swapTotal > 0 {
			sw = int(s.swapUsed * 100 / s.swapTotal)
		}
		row(2, pal(sw, 25, 60)|sBar, sw, "Swap of "+gb(s.swapTotal), gb(s.swapUsed))
		row(3, pal(s.vram, 70, 90)|sBar, s.vram, "GPU memory", fmt.Sprintf("%d%%", s.vram))
		row(4, pal(int(s.psi[1]+0.5), 2, 10)|sBar, int(s.psi[1]*5), "Memory stalls", fmt.Sprintf("%.0f%%", s.psi[1]))
		for i, t := range s.top.mem[:min(4, len(s.top.mem))] {
			row(5+i, pMagenta|sBar, int(t.v/s.memTotal*100*3), short(t.name, 13), gb(t.v))
		}
	case "claude":
		c := s.cl
		state := "idle"
		if c.busy > 0 {
			state = "working"
		}
		cs = append(cs, cell{0, pCyan | sBig, 0, fmt.Sprintf("   Claude\t%d open, %s", c.sessions, state)}) // the indent leaves room for the logo
		pct := func(v int) (int, string) {
			if v < 0 {
				return 0, "--"
			}
			return v, fmt.Sprintf("%d%%", v)
		}
		v, t := pct(c.ctx)
		row(1, pal(v, 60, 80)|sBar, v, "Context", t)
		v, t = pct(c.s5h)
		row(2, pal(v, 60, 80)|sBar, v, "5-hour session", t)
		// a live countdown to the session reset: it ticks every second
		left := time.Until(c.sessionResetAt)
		if c.sessionResetAt.IsZero() {
			row(3, pGrey, 0, "Session resets in", "--")
		} else {
			cc := byte(pWhite)
			if left < 15*time.Minute {
				cc = pOrange
			}
			row(3, cc, 0, "Session resets in", clock(left))
		}
		v, t = pct(c.wk)
		row(4, pal(v, 60, 80)|sBar, v, "Weekly used", t)
		o := weeklyOutlook(c, time.Now())
		if !o.ok {
			row(5, pGrey, 0, "Week resets in", "--")
		} else {
			row(5, pWhite, 0, "Week resets in", fmt.Sprintf("%s, %.0f%% left", longLeft(o.remaining), o.left))
			// what the week still allows, against what you have been using
			if o.early {
				row(6, pWhite, 0, "Can burn per day", fmt.Sprintf("%.0f%%", o.allowedPerDay))
			} else {
				row(6, pWhite, 0, "Can burn per day", fmt.Sprintf("%.0f%% (now %.0f%%)", o.allowedPerDay, o.usedPerDay))
			}
			if o.early { // too little of the week has passed for a meaningful projection
				row(7, pGrey, 0, "At your pace", "too early")
			} else {
				row(7, pal(int(o.proj), 80, 100)|sBar, int(o.proj), "At your pace", fmt.Sprintf("%.0f%%", o.proj))
				switch {
				case o.limitIn > 0 && o.limitIn < o.remaining: // the limit comes before the reset
					row(8, pRed, 0, "Outlook", "limit in "+longLeft(o.limitIn))
				case o.proj >= 85:
					row(8, pYellow, 0, "Outlook", fmt.Sprintf("tight, %.0f%% spare", 100-o.proj))
				default:
					row(8, pGreen, 0, "Outlook", fmt.Sprintf("on track, %.0f%% spare", 100-o.proj))
				}
			}
		}
		if !c.fetchedAt.IsZero() && time.Since(c.fetchedAt) > 15*time.Minute { // stale numbers are worth a warning more than a forecast
			row(8, pOrange, 0, "Usage data", fmt.Sprintf("%dm old", int(time.Since(c.fetchedAt).Minutes())))
		}
	case "net": // rows on top, the traffic scope in the middle (drawn by the board), the live rates at the bottom
		cs = append(cs, cell{0, pCyan | sBig, 0, "Network\tsince boot"})
		row(1, pGreen, 0, "Downloaded", fmtBytes(s.netRx))
		row(2, pOrange, 0, "Uploaded", fmtBytes(s.netTx))
		row(3, pGreen, 0, "Down last hour", fmtBytes(s.netRxH))
		row(4, pOrange, 0, "Up last hour", fmtBytes(s.netTxH))
		row(8, pWhite, 0, "Now", fmtBits(s.downAvg)+" down  "+fmtBits(s.upAvg)+" up")
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

// clock formats a countdown as h:mm:ss (or mm:ss under an hour).
func clock(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	sec := int(d.Seconds())
	if sec >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", sec/3600, sec/60%60, sec%60)
	}
	return fmt.Sprintf("%02d:%02d", sec/60, sec%60)
}

// longLeft formats a long countdown as "1d 11h" / "5h 20m" / "40m".
func longLeft(d time.Duration) string {
	m := int(d.Minutes())
	switch {
	case m >= 24*60:
		return fmt.Sprintf("%dd %dh", m/(24*60), m/60%24)
	case m >= 60:
		return fmt.Sprintf("%dh %02dm", m/60, m%60)
	}
	return fmt.Sprintf("%dm", max(m, 0))
}

// similar: the same cell up to a one-point change of the bar. Not worth sending: the bar would move by 2 px.
func similar(a, b cell) bool {
	d := int(a.pct) - int(b.pct)
	return a.slot == b.slot && a.style == b.style && a.text == b.text && d >= -1 && d <= 1
}

// outlook is the weekly picture: what is left, what the week still allows per day, where the current pace ends up
// at the reset, and whether the limit would be reached before it.
type outlook struct {
	ok, early     bool
	remaining     time.Duration
	left          float64 // % of the weekly limit still available
	allowedPerDay float64 // % per day you could burn from now and just reach the limit at the reset
	usedPerDay    float64 // % per day you have averaged so far this week
	proj          float64 // % of the limit reached at the reset if you keep that average
	limitIn       time.Duration
}

func weeklyOutlook(c claudeInfo, now time.Time) outlook {
	const week = 7 * 24 * time.Hour
	if c.wk < 0 || c.weeklyResetAt.IsZero() {
		return outlook{}
	}
	rem := c.weeklyResetAt.Sub(now)
	if rem <= 0 || rem > week {
		return outlook{}
	}
	o := outlook{ok: true, remaining: rem, left: max(0, 100-float64(c.wk))}
	o.allowedPerDay = o.left / (rem.Hours() / 24)
	elapsed := week - rem
	o.usedPerDay = float64(c.wk) / (elapsed.Hours() / 24)
	if elapsed < 6*time.Hour {
		o.early = true
		return o
	}
	perHour := float64(c.wk) / elapsed.Hours()
	o.proj = float64(c.wk) + perHour*rem.Hours()
	if perHour > 0 {
		o.limitIn = time.Duration(o.left / perHour * float64(time.Hour))
	}
	return o
}
