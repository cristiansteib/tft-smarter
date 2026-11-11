package main

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// row y for settings row i (rows are LROW = 31 px apart, 4 px from the top)
func rowY(i int) int { return 4 + i*rowH + rowH/2 }

func openSettings(t *testing.T) *rig {
	r := newRig(t)
	r.menu(6)
	r.wantPage("settings", "")
	return r
}

func TestSettingsSteppers(t *testing.T) {
	r := openSettings(t)
	d := defaultSettings()
	r.tap(420, rowY(1)) // "Return home after": right half of the value adds 15 s
	if cfg().ReturnSec != d.ReturnSec+15 {
		t.Fatalf("ReturnSec %d, want %d", cfg().ReturnSec, d.ReturnSec+15)
	}
	r.tap(320, rowY(1)) // left half takes 15 s off
	r.tap(320, rowY(1))
	if cfg().ReturnSec != d.ReturnSec-15 {
		t.Fatalf("ReturnSec %d, want %d", cfg().ReturnSec, d.ReturnSec-15)
	}
	r.tap(200, rowY(1)) // the label of a stepper row does nothing
	if cfg().ReturnSec != d.ReturnSec-15 {
		t.Fatal("tapping a stepper's label changed it")
	}
	for i := 0; i < 10; i++ { // clamped at 15 s
		r.tap(320, rowY(1))
	}
	if cfg().ReturnSec != 15 {
		t.Fatalf("ReturnSec %d, want the 15 s floor", cfg().ReturnSec)
	}
	r.tap(420, rowY(2))
	if cfg().DwellSec != d.DwellSec+1 {
		t.Fatalf("DwellSec %d", cfg().DwellSec)
	}

	r.tap(100, rowY(7)) // footer: next sub-page (thresholds)
	if r.p.sub != 1 {
		t.Fatalf("sub-page %d, want 1", r.p.sub)
	}
	r.tap(420, rowY(1))
	r.tap(420, rowY(2))
	r.tap(320, rowY(3))
	r.tap(320, rowY(4))
	c := cfg()
	if c.HogCPU != d.HogCPU+5 || c.LoadTrigger != d.LoadTrigger+5 || c.HogMem != d.HogMem-1 || c.RamTrigger != d.RamTrigger-5 {
		t.Fatalf("thresholds %+v", c)
	}
	r.tap(420, rowY(5)) // below the last row of this sub-page: nothing
	if cfg().HogCPU != c.HogCPU || cfg().LoadTrigger != c.LoadTrigger {
		t.Fatal("an empty row changed a setting")
	}

	// what the rows show
	cs := r.p.buildSettings(1)
	if cs[0].text != "Settings\tThresholds 2/3" || cs[1].text != "CPU hog above\t<  45%  >" || cs[4].text != "RAM pages when used\t<  75%  >" {
		t.Fatalf("rows %q %q %q", cs[0].text, cs[1].text, cs[4].text)
	}
	if cs[len(cs)-1].slot != 8 || cs[len(cs)-1].text != "Next page\t2/3  >" {
		t.Fatalf("footer %+v", cs[len(cs)-1])
	}

	// everything is saved at once, under the (test) config dir
	b, err := os.ReadFile(cfgPath())
	if err != nil {
		t.Fatal(err)
	}
	var saved settings
	if json.Unmarshal(b, &saved) != nil || saved.HogCPU != c.HogCPU || saved.ReturnSec != 15 {
		t.Fatalf("saved %s", b)
	}
	if !strings.HasPrefix(cfgPath(), os.Getenv("XDG_CONFIG_HOME")) {
		t.Fatalf("settings written to %s, outside the test dir", cfgPath())
	}
}

func TestSettingsPagesAndActions(t *testing.T) {
	r := openSettings(t)
	r.swipe('L')
	r.swipe('L')
	if r.p.sub != 2 {
		t.Fatalf("two swipes: sub-page %d, want 2", r.p.sub)
	}
	before := cfg()
	r.tap(100, rowY(3)) // "Network page": the whole row toggles
	if !cfg().Disabled["net"] {
		t.Fatal("net should be disabled")
	}
	if before.Disabled["net"] {
		t.Fatal("an earlier cfg() copy changed under its holder")
	}
	if got := r.p.buildSettings(2)[3].text; got != "Network page\tOff" {
		t.Fatalf("row shows %q", got)
	}
	r.tap(400, rowY(3))
	if cfg().Disabled["net"] {
		t.Fatal("net should be enabled again")
	}
	r.swipe('L') // wraps round to the first sub-page
	if r.p.sub != 0 {
		t.Fatalf("sub-page %d, want 0", r.p.sub)
	}

	// Calibrate touch: the board is asked to run its calibration
	r.run(2 * time.Second)
	r.sent = nil
	r.tap(100, rowY(3))
	r.run(time.Second)
	if !sent(r, cmdFrame(cmdCalibrate)) {
		t.Fatalf("no calibrate command sent: % x", r.sent)
	}

	// Redraw screen: a forced repaint of the current screen (mode | 0x40)
	r.sent = nil
	r.tap(100, rowY(4))
	r.run(time.Second)
	if !sent(r, screenFrame(screenMode("settings", true)|0x40)) {
		t.Fatalf("redraw sent % x", r.sent)
	}
}

func sent(r *rig, f []byte) bool {
	for _, x := range r.sent {
		if bytes.Equal(x, f) {
			return true
		}
	}
	return false
}

func TestSettingsPageRoundTrip(t *testing.T) {
	resetGlobals(t)
	c := cfg()
	c.ReturnSec, c.HogCPU, c.Pin, c.Disabled = 0, 9999, "net", map[string]bool{"docker": true}
	setCfg(c) // out-of-range values are clamped on the way in
	got := cfg()
	if got.ReturnSec != 15 || got.HogCPU != 800 || got.Pin != "net" || !got.Disabled["docker"] {
		t.Fatalf("after setCfg: %+v", got)
	}
	c.Disabled["docker"] = false // the caller's map is not the stored one
	if !cfg().Disabled["docker"] {
		t.Fatal("setCfg kept a reference to the caller's map")
	}
	cfgVal.Store(&settings{}) // loadCfg reads the file back with its defaults filled in
	loadCfg()
	if l := cfg(); l.HogCPU != 800 || l.Pin != "net" || !l.Disabled["docker"] || l.ReturnSec != 15 {
		t.Fatalf("loaded %+v", l)
	}
	b, _ := os.ReadFile(cfgPath())
	for _, k := range []string{"hogCpu", "hogMem", "loadTrigger", "ramTrigger", "creditStepMs", "dwellSec", "returnSec", "disabled", "pin"} {
		if !bytes.Contains(b, []byte(`"`+k+`"`)) {
			t.Errorf("settings.json lost the %q field", k)
		}
	}
}

// Run with -race: the control socket's goroutines read and write the settings while the run loop uses them.
func TestSettingsConcurrentAccess(t *testing.T) {
	resetGlobals(t)
	var wg sync.WaitGroup
	for g := 0; g < 4; g++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				c := cfg()
				c.Disabled = map[string]bool{"net": i%2 == 0}
				c.HogCPU = float64(10 + i)
				setCfg(c)
			}
		}()
		go func() {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				c := cfg()
				for range c.Disabled {
				}
				_ = c.HogCPU + float64(c.ReturnSec)
				json.Marshal(c)
			}
		}()
	}
	wg.Wait()
}

func TestControlCommands(t *testing.T) {
	resetGlobals(t)
	e := newEngine()
	call := func(req string) ctlResp {
		t.Helper()
		var r ctlReq
		if err := json.Unmarshal([]byte(req), &r); err != nil {
			t.Fatal(err)
		}
		return handleCtl(r, e)
	}
	if r := call(`{"cmd":"state"}`); !r.OK || r.Data == nil {
		t.Fatalf("state: %+v", r)
	}
	if r := call(`{"cmd":"settings"}`); !r.OK || r.Data.(settings).ReturnSec != 60 {
		t.Fatalf("settings: %+v", r)
	}
	if r := call(`{"cmd":"set","settings":{"hogCpu":55,"hogMem":8,"loadTrigger":60,"ramTrigger":80,"creditStepMs":1400,"dwellSec":12,"returnSec":90,"disabled":{"top":true},"pin":""}}`); !r.OK || cfg().ReturnSec != 90 || !cfg().Disabled["top"] {
		t.Fatalf("set: %+v %+v", r, cfg())
	}
	if r := call(`{"cmd":"set"}`); r.OK || r.Error == "" {
		t.Fatal("set without settings must fail")
	}
	if r := call(`{"cmd":"pin","page":"net"}`); !r.OK || cfg().Pin != "net" {
		t.Fatalf("pin: %+v", r)
	}
	if r := call(`{"cmd":"pin","page":"nope"}`); r.OK || cfg().Pin != "net" {
		t.Fatalf("pin of an unknown page: %+v", r)
	}
	if r := call(`{"cmd":"pin","page":""}`); !r.OK || cfg().Pin != "" {
		t.Fatalf("unpin: %+v", r)
	}
	if r := call(`{"cmd":"goto","page":"settings"}`); !r.OK {
		t.Fatalf("goto settings: %+v", r)
	}
	if ev := <-touchCh; ev.kind != 'g' || ev.page != "settings" {
		t.Fatalf("goto queued %+v", ev)
	}
	if r := call(`{"cmd":"goto","page":"nowhere"}`); r.OK {
		t.Fatal("goto of an unknown page must fail")
	}
	call(`{"cmd":"next"}`)
	call(`{"cmd":"prev"}`)
	if !skipAt.Load() || !prevAt.Load() {
		t.Fatal("next / prev not flagged")
	}
	if r := call(`{"cmd":"notify","text":""}`); r.OK {
		t.Fatal("empty notify must fail")
	}
	if r := call(`{"cmd":"notify","sev":"crit","ttl":1,"text":"build finished"}`); !r.OK {
		t.Fatalf("notify: %+v", r)
	}
	e.eval(fixture(), time.Now())
	list, take, _ := e.view(time.Now())
	if len(list) != 1 || list[0].sev != sevCrit || list[0].title != "build finished" || !take {
		t.Fatalf("notification: %+v take=%v", list, take)
	}
	if r := call(`{"cmd":"bogus"}`); r.OK || r.Error != "unknown cmd" {
		t.Fatalf("unknown: %+v", r)
	}

	// the state document keeps the field names the desktop app reads
	p, s := newPanel(), fixture()
	now := time.Now()
	p.tick(now, s, e)
	b, _ := json.Marshal(p.doc(s, e, 1, 2, 3, now))
	for _, k := range []string{`"connected"`, `"gauges"`, `"cpuT"`, `"gpuLimit"`, `"latency"`, `"down"`, `"uptime"`, `"load"`, `"ncpu"`, `"disks"`, `"psi"`,
		`"topCpu"`, `"topMem"`, `"panel"`, `"page"`, `"subs"`, `"pin"`, `"full"`, `"pages"`, `"eligible"`, `"enabled"`, `"cells"`, `"slot"`, `"style"`, `"pct"`, `"text"`,
		`"alerts"`, `"sev"`, `"docker"`, `"hog"`} {
		if !bytes.Contains(b, []byte(k)) {
			t.Errorf("state document lost %s", k)
		}
	}
}
