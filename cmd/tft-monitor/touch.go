package main

import (
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// Touch events arrive from the board as text lines: "T t x y" (tap), "T l x y" (long press), "T s L|R x y" (swipe).
// The reader goroutine parses them into touchCh; the panel drains them on its own tick, so no locking is needed.
type touchEv struct {
	kind byte // 't' tap, 'l' long press, 's' swipe
	dir  byte // swipe: 'L' or 'R'
	x, y int
	page string // kind 'g': go to this page (from the control socket)
}

var (
	touchCh = make(chan touchEv, 16)
	prevAt  atomic.Bool // "previous page" from the UI
)

func parseTouch(line string) (touchEv, bool) {
	f := strings.Fields(line)
	if len(f) >= 2 && f[0] == "T" && f[1] == "r" { // the board repainted itself: send everything again
		return touchEv{kind: 'r'}, true
	}
	if len(f) == 3 && f[0] == "T" && f[1] == "m" { // bottom menu choice
		i, err := strconv.Atoi(f[2])
		return touchEv{kind: 'm', x: i}, err == nil
	}
	if len(f) < 4 || f[0] != "T" {
		return touchEv{}, false
	}
	ev := touchEv{kind: f[1][0]}
	rest := f[2:]
	if ev.kind == 's' {
		if len(rest) < 3 {
			return touchEv{}, false
		}
		ev.dir, rest = rest[0][0], rest[1:]
	}
	x, err1 := strconv.Atoi(rest[0])
	y, err2 := strconv.Atoi(rest[1])
	if err1 != nil || err2 != nil {
		return touchEv{}, false
	}
	ev.x, ev.y = x, y
	return ev, true
}

func queueTouch(ev touchEv) {
	select {
	case touchCh <- ev:
	default: // never block the serial reader; a dropped tap is harmless
	}
}

// returnHold is how long a page you opened stays without a touch before the screen goes back home.
func returnHold() time.Duration { return time.Duration(cfg().ReturnSec) * time.Second }

// usable reports whether a page has something to show right now.
func usable(name string, s *snapshot) bool {
	if cfg().Disabled[name] {
		return false
	}
	return !(name == "rollcpu" && len(hogsCPU(s)) == 0) && !(name == "rollmem" && len(hogsMem(s)) == 0)
}

// step moves to the next (dir=+1) or previous (-1) usable page.
func (p *panel) step(dir int, s *snapshot, now time.Time) {
	at := -1
	for i, n := range allPages {
		if n == p.name {
			at = i
		}
	}
	for k := 1; k <= len(allPages); k++ {
		if n := allPages[((at+dir*k)%len(allPages)+len(allPages))%len(allPages)]; usable(n, s) {
			p.goTo(n, now)
			return
		}
	}
}

// goTo opens a page on demand. It stays while you keep touching it and goes back home after ReturnSec without a
// touch; with a page held (long press) the hold moves to it instead and nothing returns home.
func (p *panel) goTo(name string, now time.Time) {
	if name != "settings" && cfg().Pin != "" {
		c := cfg()
		c.Pin = name
		setCfg(c)
		p.manual = ""
		return
	}
	p.manual, p.manualEnd = name, now.Add(returnHold())
}

// handleTouch turns a gesture into a page change. The screen is 480x320: header on top, gauges, scope and
// network in the middle, a band, and the board's own menu strip along the bottom (it sends "T m i" itself).
func (p *panel) handleTouch(ev touchEv, s *snapshot, now time.Time) {
	if p.manual != "" && now.Before(p.manualEnd) {
		p.manualEnd = now.Add(returnHold()) // still in use: the timer restarts
	}
	switch ev.kind {
	case 'g':
		p.goTo(ev.page, now)
		return
	case 'r':
		p.resend = true
		return
	case 'm': // bottom menu: Home CPU RAM Net Claude System Setup
		targets := [...]string{"dash", "cpu", "ram", "net", "claude", "system", "settings"}
		if ev.x >= 0 && ev.x < len(targets) {
			p.goTo(targets[ev.x], now)
		}
		return
	}
	if p.name == "settings" {
		switch ev.kind {
		case 't':
			p.touchSettings(ev.x, ev.y, now)
		case 's':
			if ev.dir == 'L' {
				p.sub = (p.sub + 1) % settingsSubs
			} else {
				p.sub = (p.sub + settingsSubs - 1) % settingsSubs
			}
		}
		return
	}
	switch ev.kind {
	case 's':
		if ev.dir == 'L' { // finger moved left: the next page slides in
			p.step(1, s, now)
		} else {
			p.step(-1, s, now)
		}
	case 'l': // hold: keep this page up (title shows "pin"), or release it
		if p.name == "" || p.name == "alerts" {
			return
		}
		c := cfg()
		if c.Pin == p.name {
			c.Pin = ""
		} else {
			c.Pin = p.name
		}
		setCfg(c)
		p.manual = ""
	case 't':
		if p.full {
			if ev.y < 36 { // title: back home
				p.goTo("dash", now)
			}
			return
		}
		switch { // the dashboard: touch a thing to open its page
		case ev.y < 22 && ev.x < 146: // "docker" in the header
			p.goTo("docker", now)
		case ev.y < 22 && ev.x >= 296, ev.x < 16 && ev.y >= 28: // "claude" in the header, or the edge bar
			p.goTo("claude", now)
		case ev.y < 165 && ev.x < 165: // CPU gauge
			p.goTo("cpu", now)
		case ev.y < 165 && ev.x < 315: // RAM gauge
			p.goTo("ram", now)
		case ev.y < 165 && ev.x >= 315: // GPU gauge
			p.goTo("system", now)
		case ev.y >= 236 && ev.y < 258 && ev.x >= 318: // the Claude usage line under the network rates
			p.goTo("claude", now)
		case ev.y < 258 && ev.x >= 318: // network
			p.goTo("net", now)
		case ev.y < 258 && ev.y >= 165: // load scope
			p.goTo("top", now)
		}
	}
}
