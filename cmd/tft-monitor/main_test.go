package main

import (
	"os"
	"testing"
	"time"
)

// TestMain points every config / state path at a throw-away directory, so no test can read or overwrite the user's
// real ~/.config/tft-monitor/settings.json (os.UserConfigDir honours XDG_CONFIG_HOME before HOME).
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "tft-monitor-test-")
	if err != nil {
		panic(err)
	}
	os.Setenv("HOME", dir)
	os.Setenv("XDG_CONFIG_HOME", dir+"/.config")
	os.Setenv("XDG_RUNTIME_DIR", dir+"/run")
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// fixture is a busy-looking but ordinary machine: nothing is hogging, no alert fires.
func fixture() *snapshot {
	return &snapshot{
		cpu: 23, ram: 41, swap: 3, cpuT: 52, gpu: 7, gpuT: 44, vram: 18,
		load: [3]int{180, 150, 120}, ncpu: 16,
		disks: []disk{{"root", 71}, {"home", 55}, {"data", 40}, {"var/lib", 12}, {"boot2", 5}},
		cl:    claudeInfo{ctx: 35, s5h: 20, s5hResetMin: 90, wk: 30, sessions: 2},
		gpuW:  40, gpuLimit: 320, nvmeT: 45,
		psi:      [3]float64{1, 0, 2},
		latency:  12,
		dock:     dockerInfo{ok: true, total: 5, up: 5, top: []nameVal{{"db", 4e8}, {"web", 2e8}}},
		top:      topInfo{cpu: []nameVal{{"firefox", 30}, {"code", 12}, {"go", 5}}, mem: []nameVal{{"firefox", 2e9}, {"code", 1e9}, {"go", 3e8}}},
		memTotal: 32e9, ramUsed: 13e9, swapUsed: 1e8, swapTotal: 8e9,
		netRx: 104_100_000_000, netTx: 12_300_000_000, netRxH: 1_500_000_000, netTxH: 200_000_000,
		downAvg: 12_345, upAvg: 287,
	}
}

// resetGlobals puts the package-level state the panel touches back to a known start.
func resetGlobals(t testing.TB) {
	t.Helper()
	setCfg(defaultSettings())
	debounce = map[string]*latch{}
	notes = &inbox{}
	skipAt.Store(false)
	prevAt.Store(false)
	for len(touchCh) > 0 {
		<-touchCh
	}
}

// run250 drives the panel like the run loop does: one tick (and state document) per 250 ms of fake time.
func run250(p *panel, s *snapshot, e *engine, now time.Time, ticks int) time.Time {
	for i := 0; i < ticks; i++ {
		now = now.Add(250 * time.Millisecond)
		p.tick(now, s, e)
		p.doc(s, e, s.downAvg, s.upAvg, 1000, now)
	}
	return now
}

func BenchmarkTick(b *testing.B) {
	resetGlobals(b)
	p, e, s := newPanel(), newEngine(), fixture()
	now := time.Now()
	now = run250(p, s, e, now, 40) // past the screen switch and the first full send
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		now = now.Add(250 * time.Millisecond)
		p.tick(now, s, e)
		p.doc(s, e, s.downAvg, s.upAvg, 1000, now)
	}
}

func BenchmarkTickNet(b *testing.B) {
	resetGlobals(b)
	p, e, s := newPanel(), newEngine(), fixture()
	now := time.Now()
	queueTouch(touchEv{kind: 'g', page: "net"})
	now = run250(p, s, e, now, 40)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		now = now.Add(250 * time.Millisecond)
		p.manualEnd = now.Add(time.Minute) // stay on the page
		p.tick(now, s, e)
		p.doc(s, e, s.downAvg, s.upAvg, 1000, now)
	}
}

// BenchmarkProcReads is the /proc work the run loop does on every 250 ms tick.
func BenchmarkProcReads(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		cpuTimes()
		netBytes()
		readMem()
		loadAvg()
		readFile("/proc/uptime")
	}
}
