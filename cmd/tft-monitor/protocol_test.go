package main

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"
)

// crcRef is a straightforward CRC-8 (poly 0x07, init 0, no reflection: CRC-8/SMBUS), the same as crcUpdate() in the
// firmware, written independently of crc8().
func crcRef(b []byte) byte {
	var c byte
	for _, x := range b {
		for i := 7; i >= 0; i-- {
			top := (c >> 7) ^ ((x >> uint(i)) & 1)
			c <<= 1
			if top == 1 {
				c ^= 0x07
			}
		}
	}
	return c
}

func TestCRC8(t *testing.T) {
	if got := crc8([]byte("123456789")); got != 0xF4 { // the CRC-8/SMBUS check value
		t.Fatalf("crc8(123456789) = %#x, want 0xf4", got)
	}
	if crc8(nil) != 0 {
		t.Fatal("crc8 of nothing must be 0")
	}
	for n := 0; n < 300; n++ {
		b := make([]byte, n%80)
		for i := range b {
			b[i] = byte(n*31 + i*7)
		}
		if crc8(b) != crcRef(b) {
			t.Fatalf("crc8 differs from the reference for %x", b)
		}
	}
}

// checkFrame verifies the envelope 0xAA 0x55 | type | len | payload | crc8(type, len, payload) and returns the payload.
func checkFrame(t *testing.T, f []byte, typ byte) []byte {
	t.Helper()
	if len(f) < 5 || f[0] != 0xAA || f[1] != 0x55 || f[2] != typ {
		t.Fatalf("bad header % x (want type %d)", f, typ)
	}
	if int(f[3]) != len(f)-5 {
		t.Fatalf("len byte %d, payload is %d bytes", f[3], len(f)-5)
	}
	if f[len(f)-1] != crcRef(f[2:len(f)-1]) {
		t.Fatalf("bad crc in % x", f)
	}
	return f[4 : len(f)-1]
}

func TestLineAndCellFrames(t *testing.T) {
	c := cell{3, pGreen | sBar, 42, "Down last hour\t1.50 GB"}
	p := checkFrame(t, lineFrame(c), frameLine)
	if !bytes.Equal(p, append([]byte{3, pGreen | sBar, 42}, "Down last hour\t1.50 GB"...)) {
		t.Fatalf("line payload % x", p)
	}
	q := checkFrame(t, cellFrame(c), frameCell)
	if !bytes.Equal(p, q) {
		t.Fatal("a cell and a page row must carry the same payload")
	}

	// what the board's fonts cannot draw becomes '?', byte by byte (UTF-8 too); the tab stays
	p = checkFrame(t, lineFrame(cell{1, 0, 0, "Temp\t52°C\x01\n"}), frameLine)
	if string(p[3:]) != "Temp\t52??C??" {
		t.Fatalf("sanitised text %q", p[3:])
	}

	// the text is cut at 70 bytes (the board keeps 72)
	p = checkFrame(t, cellFrame(cell{0, 0, 0, strings.Repeat("x", 100)}), frameCell)
	if len(p) != 3+70 {
		t.Fatalf("long text: %d payload bytes, want 73", len(p))
	}

	// an empty cell blanks its slot: header only
	if p := checkFrame(t, cellFrame(cell{slot: 2}), frameCell); !bytes.Equal(p, []byte{2, 0, 0}) {
		t.Fatalf("blank cell % x", p)
	}
}

// The offsets here are the ones handleFrame() in src/main.cpp reads.
func TestStatsFrameLayout(t *testing.T) {
	p := checkFrame(t, statsFrame(55, 41, 7, 63, 120, 70, 300, 12345, -5, 86400*3+7, 23, 59, [3]int{180, 70000, -1}, 16, 250, 320), frameStats)
	if len(p) != 32 {
		t.Fatalf("stats payload is %d bytes, the board wants 32", len(p))
	}
	want := []byte{55, 41, 63, 100, 70, 100} // cpu ram cpuT gpu(clamped) gpuT vram(clamped)
	if !bytes.Equal(p[:6], want) {
		t.Fatalf("gauges % x, want % x", p[:6], want)
	}
	le32 := binary.LittleEndian.Uint32
	le16 := binary.LittleEndian.Uint16
	if le32(p[6:]) != 12345 || le32(p[10:]) != 0 || le32(p[14:]) != 86400*3+7 {
		t.Fatalf("down/up/uptime %d %d %d", le32(p[6:]), le32(p[10:]), le32(p[14:]))
	}
	if p[18] != 23 || p[19] != 59 || p[20] != 7 {
		t.Fatalf("hh mm swap % x", p[18:21])
	}
	if le16(p[21:]) != 180 || le16(p[23:]) != 65535 || le16(p[25:]) != 0 {
		t.Fatalf("loads %d %d %d", le16(p[21:]), le16(p[23:]), le16(p[25:]))
	}
	if p[27] != 16 || le16(p[28:]) != 250 || le16(p[30:]) != 320 {
		t.Fatalf("ncpu / gpu power % x", p[27:])
	}
}

func TestClaudeFrameLayout(t *testing.T) {
	p := checkFrame(t, claudeFrame(claudeInfo{ctx: 35, s5h: -1, s5hResetMin: 299, wk: 61, busy: 1}, -1), frameClaude)
	want := []byte{35, 0xFF, 299 & 0xFF, 299 >> 8, 61, 1, 0xFF}
	if !bytes.Equal(p, want) {
		t.Fatalf("claude payload % x, want % x", p, want)
	}
}

func TestSmallFrames(t *testing.T) {
	if p := checkFrame(t, bandFrame(2, 5, sevCrit, true, true), frameBand); !bytes.Equal(p, []byte{2, 5, 3 | 4 | 8}) {
		t.Fatalf("band % x", p)
	}
	if p := checkFrame(t, bandFrame(0, 1, sevWarn, false, false), frameBand); !bytes.Equal(p, []byte{0, 1, 2}) {
		t.Fatalf("band % x", p)
	}
	if p := checkFrame(t, pageFrame(3, true), framePage); !bytes.Equal(p, []byte{3, 1}) {
		t.Fatalf("page % x", p)
	}
	if p := checkFrame(t, cmdFrame(cmdCalibrate), frameCmd); !bytes.Equal(p, []byte{1}) {
		t.Fatalf("cmd % x", p)
	}
	if p := checkFrame(t, byeFrame(2, 7, 5), frameBye); !bytes.Equal(p, []byte{2, 7, 5}) {
		t.Fatalf("bye % x", p)
	}
	p := checkFrame(t, summaryFrame(3600, 1234, [5]int{99, 300, 81, 70, 12}, 1<<40, 5, 150), frameSummary)
	if len(p) != 22 || binary.LittleEndian.Uint32(p) != 3600 || binary.LittleEndian.Uint32(p[4:]) != 1234 ||
		!bytes.Equal(p[8:13], []byte{99, 255, 81, 70, 12}) || binary.LittleEndian.Uint32(p[13:]) != 1<<32-1 ||
		binary.LittleEndian.Uint32(p[17:]) != 5 || p[21] != 100 {
		t.Fatalf("summary % x", p)
	}
}

func TestScreenModeAndMenuSlots(t *testing.T) {
	for name, want := range map[string]byte{"dash": 0, "alerts": 0, "cpu": 1, "settings": 1, "rollcpu": 0x11, "claude": 0x21, "net": 0x09} {
		if got := screenMode(name, false); got != want {
			t.Errorf("screenMode(%s) = %#x, want %#x", name, got, want)
		}
	}
	if screenMode("cpu", true) != 2 || screenMode("net", true) != 0x0A || screenMode("dash", true) != 0 {
		t.Error("a cleared full page is mode 2 (plus its flags); the dashboard is always 0")
	}
	// the board's menu: Home CPU RAM Net Claude System Setup
	for i, n := range []string{"dash", "cpu", "ram", "net", "claude", "system", "settings"} {
		if menuSlot(n) != byte(i) {
			t.Errorf("menuSlot(%s) = %d, want %d", n, menuSlot(n), i)
		}
	}
	for _, n := range []string{"docker", "top", "rollcpu", "rollmem", "alerts", ""} {
		if menuSlot(n) != 255 {
			t.Errorf("%s has no menu entry", n)
		}
	}
}
