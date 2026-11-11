package main

import (
	"testing"
	"time"
)

func TestParseTouch(t *testing.T) {
	for line, want := range map[string]touchEv{
		"T t 120 45\r\n": {kind: 't', x: 120, y: 45},
		"T l 3 299\n":    {kind: 'l', x: 3, y: 299},
		"T s L 400 100":  {kind: 's', dir: 'L', x: 400, y: 100},
		"T s R 10 20\n":  {kind: 's', dir: 'R', x: 10, y: 20},
		"T m 4\n":        {kind: 'm', x: 4},
		"T r\n":          {kind: 'r'},
	} {
		got, ok := parseTouch(line)
		if !ok || got != want {
			t.Errorf("parseTouch(%q) = %+v %v, want %+v", line, got, ok, want)
		}
	}
	for _, line := range []string{"", "ok=120 bad=0\n", "TFT ID: 0x9486\n", "T t 12\n", "T t x 5\n", "T s L 4\n", "T m\n", "T m one\n", "t t 1 2\n"} {
		if ev, ok := parseTouch(line); ok {
			t.Errorf("parseTouch(%q) accepted %+v", line, ev)
		}
	}
}

// rig runs the panel the way the run loop does, on a fake clock: one tick per 250 ms, alert rules once a second.
type rig struct {
	t    *testing.T
	p    *panel
	e    *engine
	s    *snapshot
	now  time.Time
	n    int
	sent [][]byte // every frame the panel sent
}

func newRig(t *testing.T) *rig {
	resetGlobals(t)
	r := &rig{t: t, p: newPanel(), e: newEngine(), s: fixture(), now: time.Date(2026, 11, 11, 10, 0, 0, 0, time.Local)}
	r.run(8 * time.Second)
	if r.p.name != "dash" {
		t.Fatalf("the panel starts on %q, want the dashboard", r.p.name)
	}
	return r
}

func (r *rig) run(d time.Duration) {
	for end := r.now.Add(d); r.now.Before(end); {
		r.now = r.now.Add(250 * time.Millisecond)
		if r.n++; r.n%4 == 0 {
			r.e.eval(r.s, r.now)
		}
		r.sent = append(r.sent, r.p.tick(r.now, r.s, r.e)...)
	}
}

func (r *rig) touch(ev touchEv) {
	ev.at = r.now
	touchCh <- ev
	r.run(250 * time.Millisecond)
}

func (r *rig) tap(x, y int)  { r.touch(touchEv{kind: 't', x: x, y: y}) }
func (r *rig) menu(i int)    { r.touch(touchEv{kind: 'm', x: i}) }
func (r *rig) swipe(d byte)  { r.touch(touchEv{kind: 's', dir: d, x: 240, y: 150}) }
func (r *rig) hold()         { r.touch(touchEv{kind: 'l', x: 240, y: 150}) }
func (r *rig) pin() string   { return cfg().Pin }
func (r *rig) want(n string) { r.t.Helper(); r.wantPage(n, r.pin()) }

func (r *rig) wantPage(name, pin string) {
	r.t.Helper()
	if r.p.name != name || r.pin() != pin {
		r.t.Fatalf("at +%v: page %q pinned %q, want page %q pinned %q", r.now.Sub(time.Date(2026, 11, 11, 10, 0, 0, 0, time.Local)), r.p.name, r.pin(), name, pin)
	}
}

func (r *rig) doubleTap(x, y int) {
	r.tap(x, y)
	r.now = r.now.Add(-100 * time.Millisecond) // the second tap 150 ms after the first
	r.tap(x+4, y+3)
}

func TestDashboardTouchZones(t *testing.T) {
	for _, c := range []struct {
		x, y int
		page string
	}{
		{60, 10, "docker"},   // "docker" in the header
		{320, 10, "claude"},  // "claude" in the header
		{5, 150, "claude"},   // the edge bar
		{90, 90, "cpu"},      // CPU gauge
		{240, 90, "ram"},     // RAM gauge
		{390, 90, "system"},  // GPU gauge
		{400, 200, "net"},    // network rates
		{400, 245, "claude"}, // the Claude usage line under the network
		{150, 210, "top"},    // load scope
		{200, 10, "ram"},     // the middle of the header is the RAM gauge's column
		{100, 280, "dash"},   // nor does the band
	} {
		r := newRig(t)
		r.tap(c.x, c.y)
		r.run(time.Second)
		if r.p.name != c.page {
			t.Errorf("tap at %d,%d opened %q, want %q", c.x, c.y, r.p.name, c.page)
		}
		if r.pin() != "" {
			t.Errorf("a single tap at %d,%d pinned %q", c.x, c.y, r.pin())
		}
	}
}

func TestOnDemandPageReturnsHome(t *testing.T) {
	r := newRig(t)
	r.tap(90, 90)
	r.want("cpu")
	ret := time.Duration(cfg().ReturnSec) * time.Second
	r.run(ret - 2*time.Second)
	r.want("cpu")
	r.run(minFull + 3*time.Second) // after ReturnSec without a touch (a full page stays its 10 s minimum)
	r.want("dash")

	// every touch on the page restarts the timer
	r.tap(240, 90)
	r.want("ram")
	for i := 0; i < 4; i++ {
		r.run(ret / 2)
		r.tap(200, 150) // in the body: nothing happens but the timer restarts
		r.want("ram")
	}
	r.run(ret + minFull + time.Second)
	r.want("dash")

	// the title takes you home at once
	r.menu(3)
	r.want("net")
	r.run(2 * time.Second)
	r.tap(100, 20)
	r.run(time.Second)
	r.want("dash")
}

func TestTapActiveTabPins(t *testing.T) {
	r := newRig(t)
	r.menu(1)
	r.wantPage("cpu", "")
	r.menu(1) // the highlighted entry: pin
	r.wantPage("cpu", "cpu")
	r.run(10 * time.Minute) // no return home
	r.wantPage("cpu", "cpu")
	r.menu(1) // again: let go
	r.wantPage("cpu", "")
	r.run(time.Duration(cfg().ReturnSec)*time.Second + minFull + time.Second)
	r.wantPage("dash", "")

	r.menu(0) // even home can be pinned
	r.wantPage("dash", "dash")
	r.menu(0)
	r.wantPage("dash", "")
}

func TestDoubleTapPins(t *testing.T) {
	r := newRig(t)
	r.doubleTap(240, 90) // a gauge: the first tap opens its page, the second pins it
	r.wantPage("ram", "ram")
	r.run(5 * time.Minute)
	r.wantPage("ram", "ram")
	r.doubleTap(200, 150) // again in the body: let go
	r.wantPage("ram", "")

	r.hold() // a long press does the same
	r.wantPage("ram", "ram")
	r.hold()
	r.wantPage("ram", "")

	// two taps too far apart in time or space are two taps, not a double tap
	r = newRig(t)
	r.tap(240, 90)
	r.run(time.Second)
	r.tap(240, 90)
	r.wantPage("ram", "")
	r.tap(100, 100)
	r.now = r.now.Add(-100 * time.Millisecond)
	r.tap(300, 200)
	r.wantPage("ram", "")
	// a tap on the title never counts as the second one of a pair: it goes home
	r.tap(100, 100)
	r.now = r.now.Add(-100 * time.Millisecond)
	r.tap(102, 30)
	r.run(time.Second)
	r.wantPage("dash", "")
}

func TestNavigationReleasesPin(t *testing.T) {
	r := newRig(t)
	r.menu(3)
	r.menu(3)
	r.wantPage("net", "net")
	r.menu(2) // somewhere else: the pin goes
	r.wantPage("ram", "")

	r.menu(2)
	r.wantPage("ram", "ram")
	r.swipe('L') // swiping moves on too
	r.wantPage("net", "")
	r.swipe('R')
	r.wantPage("ram", "")

	r.menu(2)
	r.touch(touchEv{kind: 'g', page: "docker"}) // goto from the desktop app
	r.wantPage("docker", "")
}

func TestPinnedPageIgnoresAlerts(t *testing.T) {
	r := newRig(t)
	r.menu(3)
	r.menu(3)
	r.s.disks = []disk{{"root", 97}, {"home", 55}}
	r.run(30 * time.Second)
	r.wantPage("net", "net")
	if len(r.p.doc(r.s, r.e, 0, 0, 0, r.now).Alerts) == 0 {
		t.Fatal("the disk alert should be active")
	}

	r.menu(3) // let go: the alert takes over the dashboard band (its takeover restarts with a new alert)
	r.s.disks = []disk{{"root", 97}, {"data", 96}}
	r.run(3 * time.Second)
	r.run(minFull)
	r.wantPage("alerts", "")

	r.menu(0) // home pinned: alerts are a state of the dashboard, they still show
	r.menu(0)
	if r.pin() != "dash" {
		t.Fatalf("pin %q, want dash", r.pin())
	}
	r.s.disks = []disk{{"root", 97}, {"data", 96}, {"var", 95}}
	r.run(3 * time.Second)
	r.wantPage("alerts", "dash")
}

func TestSettingsKeepsPin(t *testing.T) {
	r := newRig(t)
	r.menu(1)
	r.menu(1)
	r.wantPage("cpu", "cpu")
	r.menu(6) // Setup does not let go of the pin
	r.wantPage("settings", "cpu")
	r.tap(420, 40) // change something
	r.wantPage("settings", "cpu")
	r.tap(100, 10) // leave by the title: the pinned page is still there underneath
	r.wantPage("cpu", "cpu")

	r.menu(6)
	r.menu(1) // from Setup to the pinned page itself: still pinned
	r.wantPage("cpu", "cpu")
	r.menu(1)
	r.wantPage("cpu", "")
	r.menu(6) // without a pin the title goes home
	r.tap(100, 10)
	r.wantPage("dash", "")

	r.menu(6) // the settings themselves pinned: leaving lets go
	r.menu(6)
	r.wantPage("settings", "settings")
	r.run(5 * time.Minute)
	r.wantPage("settings", "settings")
	r.tap(100, 10)
	r.wantPage("dash", "")
}

func TestSwipeSkipsIdleHogPages(t *testing.T) {
	r := newRig(t)
	r.menu(1)
	r.swipe('L')
	r.want("ram")
	r.swipe('R')
	r.want("cpu")
	r.touch(touchEv{kind: 'g', page: "top"})
	r.swipe('L') // the hog pages have nobody to show: wrap round to home
	r.want("dash")
	r.swipe('R') // and back past them from home
	r.want("top")

	cfgSet(t, func(c *settings) { c.Disabled = map[string]bool{"ram": true} })
	r.touch(touchEv{kind: 'g', page: "cpu"})
	r.swipe('L') // a page switched off in the settings is skipped
	r.want("net")
}

func cfgSet(t *testing.T, f func(*settings)) {
	t.Helper()
	c := cfg()
	f(&c)
	setCfg(c)
}
