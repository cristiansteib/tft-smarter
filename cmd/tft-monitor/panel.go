package main

import (
	"fmt"
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

func subPages(name string, s *snapshot) int {
	if name == "disks" {
		return max(1, (len(s.disks)+3)/4)
	}
	if name == "system" {
		return 2
	}
	return 1
}

func dwell(name string, sub int) time.Duration {
	return 9 * time.Second
}

func groupNames(s *snapshot, alerts bool) []string {
	var n []string
	if alerts {
		n = append(n, "alerts")
	}
	n = append(n, "disks")
	if s.dock.ok && s.dock.total > 0 {
		n = append(n, "docker")
	}
	n = append(n, "system")
	if len(s.top.cpu) > 0 {
		n = append(n, "top")
	}
	return n
}

func (p *panel) build(name string, sub int, s *snapshot, list []*alert, take bool, since, now time.Time) []cell {
	var cs []cell
	switch name {
	case "disks":
		for i := 0; i < 4; i++ {
			if idx := sub*4 + i; idx < len(s.disks) {
				d := s.disks[idx]
				cs = append(cs, cell{byte(i), pal(d.pct, 70, 90) | sBar, byte(d.pct), fmt.Sprintf("%s\t%d%%", short(d.name, 6), d.pct)})
			}
		}
	case "docker":
		d := s.dock
		w := byte(pGreen)
		if d.unhealthy+d.bad+d.restarting > 0 {
			w = pOrange
		}
		cs = append(cs, cell{0, w | sWide | sBright, 0, fmt.Sprintf("DOCKER\t%d/%d up  %d bad", d.up, d.total, d.unhealthy+d.bad+d.restarting)})
		for i, t := range d.top[:min(2, len(d.top))] {
			pct := 100.0
			if d.top[0].v > 0 {
				pct = t.v / d.top[0].v * 100
			}
			cs = append(cs, cell{byte(2 + i), pSky | sBar, byte(pct), fmt.Sprintf("%s\t%s", short(t.name, 6), gb(t.v))})
		}
	case "system":
		for i, n := range []string{"P.CPU", "P.MEM", "P.IO"} {
			v := int(s.psi[i] + 0.5)
			cs = append(cs, cell{byte(i), pal(v, 20, 50) | sBar, byte(min(v, 100)), fmt.Sprintf("%s\t%d%%", n, v)})
		}
		nv := byte(pSky)
		if s.nvmeT >= 80 {
			nv = pRed
		} else if s.nvmeT >= 70 {
			nv = pOrange
		}
		cs = append(cs, cell{3, nv | sBar, byte(min(s.nvmeT, 100)), fmt.Sprintf("NVME\t%dC", s.nvmeT)})
		cs = append(cs, cell{4, pCyan | sBar, byte(min(s.gpuW*100/max(s.gpuLimit, 1), 100)), fmt.Sprintf("GPU W\t%dW", s.gpuW)})
		if s.latency < 0 {
			cs = append(cs, cell{5, pRed | sBar, 0, "PING\tFAIL"})
		} else {
			cs = append(cs, cell{5, pal(s.latency, 50, 150) | sBar, byte(min(s.latency, 100)), fmt.Sprintf("PING\t%dms", s.latency)})
		}
		cs = append(cs, cell{6, pGrey, 0, fmt.Sprintf("PC NOW\t~%dW", s.pcW)})
		cs = append(cs, cell{7, pGrey, 0, "BOOT\t~" + wh(s.whBoot)})
		var part []cell
		for _, c := range cs {
			if int(c.slot)/4 == sub {
				c.slot %= 4
				part = append(part, c)
			}
		}
		cs = part
	case "top":
		for i, t := range s.top.cpu[:min(2, len(s.top.cpu))] {
			cs = append(cs, cell{byte(i * 2), pal(int(t.v), 50, 150) | sBar, byte(min(int(t.v), 100)), fmt.Sprintf("%s\t%.0f%%", short(t.name, 6), t.v)})
		}
		for i, t := range s.top.mem[:min(2, len(s.top.mem))] {
			cs = append(cs, cell{byte(i*2 + 1), pMagenta | sBar, byte(min(t.v/s.memTotal*100, 100)), fmt.Sprintf("%s\t%s", short(t.name, 6), gb(t.v))})
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
	if take && len(list) > 0 {
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
			if now.Sub(p.enteredAt) >= dwell(name, sub) {
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
	if changed {
		p.name, p.sub = name, sub
		if name == "alerts" && take {
			p.enteredAt = now
		}
		p.shown = map[byte]cell{}
		p.pending = [][]byte{bandFrame(0, 0, maxSev, true, true)}
		p.lastFull = now
	} else if now.Sub(p.lastFull) >= 5*time.Second {
		p.shown = map[byte]cell{}
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
	if key != p.lastBand || now.Sub(p.lastBandT) >= 5*time.Second || changed {
		p.lastBand, p.lastBandT = key, now
		out = append(out, bandFrame(idx, len(names), maxSev, false, border))
	}
	want := p.build(name, sub, s, list, take, since, now)
	have := map[byte]bool{}
	for _, c := range want {
		have[c.slot] = true
		if old, ok := p.shown[c.slot]; !ok || old != c {
			p.shown[c.slot] = c
			p.pending = append(p.pending, cellFrame(c))
		}
	}
	for slot := range p.shown { // blank cells that are no longer used
		if !have[slot] {
			delete(p.shown, slot)
			p.pending = append(p.pending, cellFrame(cell{slot: slot}))
		}
	}
	n := min(3, len(p.pending)) // pacing: the Mega draws slowly and has a small RX buffer
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
