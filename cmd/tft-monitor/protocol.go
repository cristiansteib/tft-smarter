package main

import "encoding/binary"

// Binary framing between the PC and the Arduino:
//
//	0xAA 0x55 | type | len | payload (len bytes) | crc8(type,len,payload)
//
// The Arduino resyncs on 0xAA 0x55 and drops any frame whose CRC does not match.
const (
	frameStats  = 1 // 32 bytes: cpu ram cpuT gpu gpuT vram (u8) | down up uptime (u32 LE) | hh mm swap | load1 load5 load15 (u16 LE, x100) | ncpu | gpuW gpuLimit (u16 LE)
	frameClaude = 2 // 7 bytes:  ctx s5h (i8) | resetMin (u16 LE) | wk (i8) | busy (u8) | docker (i8)
)

// crc8 is CRC-8 with polynomial 0x07 (init 0).
func crc8(b []byte) byte {
	var c byte
	for _, x := range b {
		c ^= x
		for i := 0; i < 8; i++ {
			if c&0x80 != 0 {
				c = c<<1 ^ 0x07
			} else {
				c <<= 1
			}
		}
	}
	return c
}

func frame(typ byte, payload []byte) []byte {
	out := make([]byte, 0, 5+len(payload))
	out = append(out, 0xAA, 0x55, typ, byte(len(payload)))
	out = append(out, payload...)
	return append(out, crc8(out[2:]))
}

func clamp8(v int) byte {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return byte(v)
}

func clamp100(v int) byte { return clamp8(min(v, 100)) }

func statsFrame(cpu, ram, swap, cpuT, gpu, gpuT, vram, down, up int, uptime uint32, hh, mm int, load [3]int, ncpu, gpuW, gpuLimit int) []byte {
	p := []byte{clamp100(cpu), clamp100(ram), clamp8(cpuT), clamp100(gpu), clamp8(gpuT), clamp100(vram)}
	p = binary.LittleEndian.AppendUint32(p, uint32(max(down, 0)))
	p = binary.LittleEndian.AppendUint32(p, uint32(max(up, 0)))
	p = binary.LittleEndian.AppendUint32(p, uptime)
	p = append(p, byte(hh), byte(mm), clamp100(swap))
	for _, l := range load {
		p = binary.LittleEndian.AppendUint16(p, uint16(min(max(l, 0), 65535)))
	}
	p = append(p, clamp8(ncpu))
	p = binary.LittleEndian.AppendUint16(p, uint16(min(max(gpuW, 0), 65535)))
	return frame(frameStats, binary.LittleEndian.AppendUint16(p, uint16(min(max(gpuLimit, 0), 65535))))
}

func claudeFrame(c claudeInfo, docker int) []byte {
	p := []byte{byte(int8(c.ctx)), byte(int8(c.s5h))}
	p = binary.LittleEndian.AppendUint16(p, uint16(max(c.s5hResetMin, 0)))
	return frame(frameClaude, append(p, byte(int8(c.wk)), byte(c.busy), byte(int8(docker))))
}

// Info-panel (bottom band) frames. The PC lays everything out; the Mega only draws what it is told.
const (
	frameSummary = 6  // 22 bytes: uptime s (u32) | Wh x10 (u32) | peak cpu gpu cpuT gpuT avgCpu (u8) | down MB (u32) | up MB (u32) | fullest disk %
	frameScreen  = 8  // 1 byte: 0 = dashboard (gauges + band), 1 = full-screen page, 2 = full-screen page, cleared first
	frameLine    = 9  // row (0 = title, 1..9) | style | pct | "label\tvalue" - a row of a full-screen page
	framePage    = 10 // 2 bytes: which bottom-menu entry is showing (255 = none), pinned (0/1)
	frameCmd     = 11 // 1 byte: 1 = run the touch calibration
	frameBye     = 7  // 3 bytes: reason (1 off, 2 reboot, 3 monitor stopped) | hh | mm
	frameBand    = 4  // 3 bytes: page idx | page count | flags (bits0-1 severity, bit2 clear band, bit3 border visible)
	frameCell    = 5  // slot | style | pct | "label\tvalue"
	frameColor   = 12 // 3 bytes: palette index (9 accent, 10 dim accent) | RGB565 (u16 LE) - colours of the notice page
	frameGeo     = 13 // 2 bytes: air above the notice header | offset of its content rows (px) - protocol 2
)

// Cell style: low nibble = palette index of the value; high bits are flags.
const (
	pWhite = iota
	pGrey
	pGreen
	pYellow
	pOrange
	pRed
	pCyan
	pSky
	pMagenta
)

const (
	sBar    = 1 << 4 // draw a progress bar between label and value
	sWide   = 1 << 5 // span both columns (slot must be even)
	sBig    = 1 << 6 // size-2 text
	sBright = 1 << 7 // label uses the palette colour too
	sSwap   = 1 << 5 // full-screen bar rows only: text starts with the swap % (3 digits), drawn as a thin bar under the row
)

type cell struct {
	slot, style, pct byte
	text             string
}

// textFrame encodes a cell or a page row: slot | style | pct | text, the text cut to 70 bytes and anything the
// board's fonts cannot draw (outside printable ASCII, except the tab between label and value) turned into '?'.
func textFrame(typ byte, c cell) []byte {
	t := c.text[:min(len(c.text), 70)]
	out := make([]byte, 0, 8+len(t))
	out = append(out, 0xAA, 0x55, typ, byte(3+len(t)), c.slot, c.style, c.pct)
	for i := 0; i < len(t); i++ {
		b := t[i]
		if b > 126 || (b < 32 && b != '\t') {
			b = '?'
		}
		out = append(out, b)
	}
	return append(out, crc8(out[2:]))
}

// cellFrame is a cell of the dashboard band.
func cellFrame(c cell) []byte { return textFrame(frameCell, c) }

func bandFrame(idx, count, sev int, clear, border bool) []byte {
	f := byte(sev & 3)
	if clear {
		f |= 4
	}
	if border {
		f |= 8
	}
	return frame(frameBand, []byte{byte(idx), byte(count), f})
}

func summaryFrame(uptime, wh10 uint32, peaks [5]int, downMB, upMB uint64, disk int) []byte {
	p := binary.LittleEndian.AppendUint32(nil, uptime)
	p = binary.LittleEndian.AppendUint32(p, wh10)
	for _, v := range peaks {
		p = append(p, clamp8(v))
	}
	p = binary.LittleEndian.AppendUint32(p, uint32(min(downMB, 1<<32-1)))
	p = binary.LittleEndian.AppendUint32(p, uint32(min(upMB, 1<<32-1)))
	return frame(frameSummary, append(p, clamp100(disk)))
}

func byeFrame(reason byte, hh, mm int) []byte {
	return frame(frameBye, []byte{reason, byte(hh), byte(mm)})
}

func screenFrame(mode byte) []byte { return frame(frameScreen, []byte{mode}) }

// lineFrame is a row of a full-screen page; c.slot is the row.
func lineFrame(c cell) []byte { return textFrame(frameLine, c) }

const cmdCalibrate = 1

func pageFrame(slot byte, pinned bool) []byte {
	return frame(framePage, []byte{slot, byte(b2i(pinned))})
}
func cmdFrame(cmd byte) []byte { return frame(frameCmd, []byte{cmd}) }

func colorFrame(idx byte, c uint16) []byte {
	return frame(frameColor, []byte{idx, byte(c), byte(c >> 8)})
}

// menuSlot maps a page to its entry in the board's bottom menu (Home CPU RAM Net Claude System Setup).
func menuSlot(name string) byte {
	switch name {
	case "dash":
		return 0
	case "cpu":
		return 1
	case "ram":
		return 2
	case "net":
		return 3
	case "claude":
		return 4
	case "system":
		return 5
	case "settings":
		return 6
	}
	return 255
}

func geoFrame(g noteGeo) []byte { return frame(frameGeo, g[:]) }
