package main

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func statLine(comm string, utime, stime, rss int) string {
	f := make([]string, 50)
	for i := range f {
		f[i] = "0"
	}
	f[0] = "S"
	f[11], f[12], f[21] = fmt.Sprint(utime), fmt.Sprint(stime), fmt.Sprint(rss)
	return "1234 (" + comm + ") " + strings.Join(f, " ") + "\n"
}

func TestParseStat(t *testing.T) {
	for _, comm := range []string{"bash", "Web Content", "(sd-pam)", "a) b (c", "kworker/0:1H-kblockd", ""} {
		name, ticks, rss, ok := parseStat([]byte(statLine(comm, 120, 30, 5000)))
		if !ok || string(name) != comm || ticks != 150 || rss != 5000 {
			t.Errorf("%q: name %q ticks %d rss %d ok %v", comm, name, ticks, rss, ok)
		}
	}
	short := "1 (x) S 0 0 0 0 0 0 0 0 0 0 5 6 0 0 0 0 0 0 0 0\n" // 21 fields after the name: one short
	for _, bad := range []string{"", "no parens", "1 )x( S", short} {
		if _, _, _, ok := parseStat([]byte(bad)); ok {
			t.Errorf("parseStat(%q) accepted a broken line", bad)
		}
	}
	if _, ticks, _, ok := parseStat([]byte(statLine("x", -1, 7, 1))); !ok || ticks != 7 {
		t.Errorf("a field that is not a number counts as 0: ticks %d", ticks)
	}
}

func TestScanProcsFindsItself(t *testing.T) {
	procs := scanProcs()
	if len(procs) == 0 {
		t.Skip("no /proc")
	}
	found := false
	for name, a := range procs {
		if strings.HasPrefix("tft-monitor.test", name) && a.rss > 0 { // comm is cut to 15 characters
			found = true
		}
	}
	if !found {
		t.Error("the test binary itself is not in the process list")
	}
}

func TestParseGPU(t *testing.T) {
	if g := parseGPU("17, 50, 2601, 12288, 25.04, 170.00\n"); g != (gpuInfo{17, 50, 21, 25, 170}) {
		t.Errorf("parsed %+v", g)
	}
	if g := parseGPU("3, 41, 100, 8192, [N/A], [N/A]"); g != (gpuInfo{3, 41, 1, 0, 0}) {
		t.Errorf("missing power: %+v", g)
	}
	for _, bad := range []string{"", "No devices were found", "1, 2, 3", "1, 2, 3, 0, 5, 6"} {
		if g := parseGPU(bad); g != (gpuInfo{}) {
			t.Errorf("parseGPU(%q) = %+v, want nothing", bad, g)
		}
	}
	var r gpuReader // no sample yet, or a stale one: nothing, as when there is no GPU
	if r.get() != (gpuInfo{}) {
		t.Error("an empty reader returned data")
	}
	r.last, r.at = gpuInfo{1, 2, 3, 4, 5}, time.Now()
	if r.get() != r.last {
		t.Error("a fresh sample was not returned")
	}
	r.at = time.Now().Add(-10 * time.Second)
	if r.get() != (gpuInfo{}) {
		t.Error("a stale sample was returned")
	}
}

func TestKbps(t *testing.T) {
	if got := kbps(1_000_000+125_000, 1_000_000, 1); got != 1000 { // 125 kB in a second = 1 Mbit/s
		t.Errorf("kbps = %d, want 1000", got)
	}
	if got := kbps(1_000_000+31_250, 1_000_000, 0.25); got != 1000 {
		t.Errorf("kbps over 250 ms = %d, want 1000", got)
	}
	if got := kbps(5, 1_000_000, 0.25); got != 0 { // a counter that went backwards (a NIC went away) is not 59 Pbit/s
		t.Errorf("backwards counter: kbps = %d, want 0", got)
	}
}

func TestNetHistHour(t *testing.T) {
	var h netHist
	t0 := time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)
	if rx, tx := h.hour(100, 100); rx != 0 || tx != 0 {
		t.Fatal("no history: no transfer")
	}
	rx, tx := uint64(1000), uint64(500)
	for m := 0; m <= 90; m++ { // 90 minutes, 4 ticks a minute is enough: one sample per minute is kept
		for k := 0; k < 4; k++ {
			h.update(t0.Add(time.Duration(m)*time.Minute+time.Duration(k)*15*time.Second), rx, tx)
			rx, tx = rx+250, tx+25
		}
	}
	// the oldest kept sample is from minute 30 (61 per-minute samples: 30..90); since then 61 minutes of
	// 1000 rx / 100 tx bytes a minute went by, counting the minute still running
	if dRx, dTx := h.hour(rx, tx); dRx != 61_000 || dTx != 6_100 {
		t.Fatalf("last hour: rx %d tx %d, want 61000 / 6100", dRx, dTx)
	}
}

func TestReadMem(t *testing.T) {
	m := readMem()
	if m.ramUsed <= 0 || m.ram <= 0 || m.ram > 100 || m.swap < 0 || m.swap > 100 || m.swapUsed > m.swapTotal {
		t.Fatalf("readMem: %+v", m)
	}
}
