package main

import (
	"bytes"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestFmtBits(t *testing.T) {
	for _, c := range []struct {
		kbps int
		want string
	}{
		{0, "0 Kbps"},
		{1, "1 Kbps"},
		{287, "287 Kbps"},
		{999, "999 Kbps"},
		{1000, "1.0 Mbps"},
		{1049, "1.0 Mbps"},
		{1050, "1.1 Mbps"},
		{12_345, "12.3 Mbps"},
		{99_949, "99.9 Mbps"},
		{99_950, "100 Mbps"}, // would round to "100.0": the next unit up keeps three digits
		{100_499, "100 Mbps"},
		{941_000, "941 Mbps"},
		{999_499, "999 Mbps"},
		{999_500, "1.00 Gbps"},
		{1_004_999, "1.00 Gbps"},
		{1_005_000, "1.01 Gbps"},
		{2_500_000, "2.50 Gbps"},
		{9_994_999, "9.99 Gbps"},
		{10_000_000, "10.00 Gbps"}, // the top step of the scope (10 Gbps) still fits the rule's format
	} {
		if got := fmtBits(c.kbps); got != c.want {
			t.Errorf("fmtBits(%d) = %q, want %q", c.kbps, got, c.want)
		}
	}
}

// Under 10 Gbps a rate never shows more than three significant digits, and the rounding error stays under half a
// unit of the last digit shown.
func TestFmtBitsSignificantDigits(t *testing.T) {
	for k := 0; k < 9_995_000; k += 997 {
		s := fmtBits(k)
		num, unit, _ := strings.Cut(s, " ")
		digits := strings.TrimLeft(strings.ReplaceAll(num, ".", ""), "0")
		if digits == "" {
			digits = "0"
		}
		if len(digits) > 3 {
			t.Fatalf("fmtBits(%d) = %q: more than 3 significant digits", k, s)
		}
		v, _ := strconv.ParseFloat(num, 64)
		mult := map[string]float64{"Kbps": 1, "Mbps": 1e3, "Gbps": 1e6}[unit]
		if math.Abs(v*mult-float64(k)) > 0.0051*float64(k)+0.5 { // rounding error stays under half a unit of the last digit
			t.Fatalf("fmtBits(%d) = %q is off by more than rounding", k, s)
		}
	}
}

// fmtBits must stay in lock-step with fmtRate() in the firmware: the board prints the live rates on the dashboard, the
// daemon prints them on the Network page. This compiles the C function out of src/main.cpp and compares the two.
func TestFmtBitsMatchesFirmware(t *testing.T) {
	cc, err := exec.LookPath("cc")
	if err != nil {
		t.Skip("no C compiler")
	}
	src, err := os.ReadFile("../../src/main.cpp")
	if err != nil {
		t.Skip("firmware source not found")
	}
	i := bytes.Index(src, []byte("static void fmtRate("))
	if i < 0 {
		t.Fatal("fmtRate() not found in src/main.cpp")
	}
	end := bytes.Index(src[i:], []byte("\n}\n"))
	if end < 0 {
		t.Fatal("end of fmtRate() not found")
	}
	prog := "#include <stdio.h>\n#include <stdint.h>\n#include <stddef.h>\n" +
		"#define PSTR(s) (s)\n#define snprintf_P snprintf\n" + // avr-libc's flash-string printf is plain printf here
		string(src[i:i+end+3]) +
		`int main(void) { unsigned long v; char b[32]; while (scanf("%lu", &v) == 1) { fmtRate(b, sizeof b, (uint32_t)v); puts(b); } return 0; }` + "\n"
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "rate.c"), []byte(prog), 0o644); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "rate")
	if out, err := exec.Command(cc, "-x", "c", "-std=c99", "-o", bin, filepath.Join(dir, "rate.c")).CombinedOutput(); err != nil {
		t.Fatalf("compiling fmtRate: %v\n%s", err, out)
	}
	var in strings.Builder
	var vals []int
	add := func(v int) { vals = append(vals, v); in.WriteString(strconv.Itoa(v) + "\n") }
	for v := 0; v <= 3000; v++ {
		add(v)
	}
	for _, b := range []int{99_949, 99_950, 99_951, 999_499, 999_500, 999_501, 1_004_999, 1_005_000, 9_994_999, 9_995_000, 10_000_000, 99_999_999, 4_000_000_000} {
		for d := -3; d <= 3; d++ {
			add(b + d)
		}
	}
	for v := 3000; v < 20_000_000; v = v*21/20 + 7 {
		add(v)
	}
	cmd := exec.Command(bin)
	cmd.Stdin = strings.NewReader(in.String())
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	if len(lines) != len(vals) {
		t.Fatalf("got %d lines for %d values", len(lines), len(vals))
	}
	for k, v := range vals {
		if lines[k] != fmtBits(v) {
			t.Errorf("rate %d kbit/s: board shows %q, daemon %q", v, lines[k], fmtBits(v))
		}
	}
}

func TestFmtBytes(t *testing.T) {
	for _, c := range []struct {
		b    uint64
		want string
	}{
		{0, "0 B"},
		{999, "999 B"},
		{1000, "1.00 KB"},
		{1499, "1.50 KB"},
		{9_994, "9.99 KB"},
		{9_995, "10.0 KB"},
		{45_600_000, "45.6 MB"},
		{99_949_999, "99.9 MB"},
		{99_950_000, "100 MB"},
		{999_499_999, "999 MB"},
		{999_500_000, "1.00 GB"}, // decimal units: 1 GB = 10^9 bytes
		{104_100_000_000, "104 GB"},
		{112_000_000_000, "112 GB"},
		{1_230_000_000_000, "1.23 TB"},
		{999_000_000_000_000, "999 TB"},
		{5_000_000_000_000_000, "5000 TB"}, // TB is the largest unit
	} {
		if got := fmtBytes(c.b); got != c.want {
			t.Errorf("fmtBytes(%d) = %q, want %q", c.b, got, c.want)
		}
	}
}

func TestAvgNDeadBand(t *testing.T) {
	var a avgN
	for i := 0; i < 24; i++ {
		a.add(1000)
	}
	if a.shown != 1000 {
		t.Fatalf("steady 1000: shown %v", a.shown)
	}
	for i := 0; i < 24; i++ { // +6 %: inside the dead band (1000/12 + 2 = 85.3)
		if got := a.add(1060); got != 1000 {
			t.Fatalf("1060 after 1000 moved the readout to %v", got)
		}
	}
	for i := 0; i < 12; i++ { // +13 %: the readout follows, in steps bigger than the dead band
		a.add(1200)
	}
	if a.shown < 1200*0.92 || a.shown > 1200 {
		t.Fatalf("1200: shown %v, want within the dead band below 1200", a.shown)
	}
	var z avgN
	for i := 0; i < 24; i++ { // the +2 keeps tiny rates from flickering
		if got := z.add(2); got != 0 {
			t.Fatalf("2 kbit/s moved an idle readout to %v", got)
		}
	}
	for i := 0; i < 24; i++ {
		z.add(3)
	}
	if z.shown <= 1 || z.shown > 3 { // it moves once the average is more than 2 away, and may settle up to 2 short
		t.Fatalf("3 kbit/s: shown %v", z.shown)
	}
}

func TestWeeklyOutlook(t *testing.T) {
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	day := 24 * time.Hour
	near := func(a, b float64) bool { return math.Abs(a-b) < 1e-6 }

	for name, c := range map[string]claudeInfo{
		"no usage":         {wk: -1, weeklyResetAt: now.Add(3 * day)},
		"no reset time":    {wk: 40},
		"reset passed":     {wk: 40, weeklyResetAt: now.Add(-time.Minute)},
		"reset > one week": {wk: 40, weeklyResetAt: now.Add(8 * day)},
	} {
		if o := weeklyOutlook(c, now); o.ok {
			t.Errorf("%s: ok, want no outlook", name)
		}
	}

	// two hours into the week: what is allowed, but no projection yet
	o := weeklyOutlook(claudeInfo{wk: 3, weeklyResetAt: now.Add(7*day - 2*time.Hour)}, now)
	if !o.ok || !o.early || o.proj != 0 || o.limitIn != 0 {
		t.Fatalf("early week: %+v", o)
	}
	if !near(o.left, 97) || !near(o.allowedPerDay, 97/((7*24-2)/24.0)) || !near(o.usedPerDay, 3/(2/24.0)) {
		t.Fatalf("early week numbers: %+v", o)
	}

	// half way, half used: on course to land exactly on the limit at the reset
	o = weeklyOutlook(claudeInfo{wk: 50, weeklyResetAt: now.Add(84 * time.Hour)}, now)
	if !o.ok || o.early || !near(o.proj, 100) || !near(o.allowedPerDay, o.usedPerDay) || o.limitIn != 84*time.Hour {
		t.Fatalf("half way: %+v", o)
	}

	// 60 % after three days: the limit comes two days before the reset
	o = weeklyOutlook(claudeInfo{wk: 60, weeklyResetAt: now.Add(4 * day)}, now)
	if !near(o.proj, 140) || o.limitIn != 2*day || !near(o.allowedPerDay, 10) || !near(o.usedPerDay, 20) {
		t.Fatalf("too fast: %+v", o)
	}

	// over the limit already: nothing left, and the limit is "now"
	o = weeklyOutlook(claudeInfo{wk: 104, weeklyResetAt: now.Add(day)}, now)
	if o.left != 0 || o.allowedPerDay != 0 || o.limitIn != 0 {
		t.Fatalf("over the limit: %+v", o)
	}

	// nothing used: no limit in sight
	o = weeklyOutlook(claudeInfo{wk: 0, weeklyResetAt: now.Add(2 * day)}, now)
	if o.proj != 0 || o.limitIn != 0 || !near(o.allowedPerDay, 50) {
		t.Fatalf("idle week: %+v", o)
	}
}

func TestCountdowns(t *testing.T) {
	for d, want := range map[time.Duration]string{
		-time.Second:                          "00:00",
		59 * time.Second:                      "00:59",
		61*time.Minute + 5*time.Second:        "1:01:05",
		4*time.Hour + 59*time.Minute + 59*1e9: "4:59:59",
		59*time.Minute + 59*time.Second + 9e8: "59:59",
	} {
		if got := clock(d); got != want {
			t.Errorf("clock(%v) = %q, want %q", d, got, want)
		}
	}
	for d, want := range map[time.Duration]string{
		-time.Hour:                    "0m",
		40 * time.Minute:              "40m",
		5*time.Hour + 20*time.Minute:  "5h 20m",
		5*time.Hour + 5*time.Minute:   "5h 05m",
		35 * time.Hour:                "1d 11h",
		6*24*time.Hour + 23*time.Hour: "6d 23h",
	} {
		if got := longLeft(d); got != want {
			t.Errorf("longLeft(%v) = %q, want %q", d, got, want)
		}
	}
}
