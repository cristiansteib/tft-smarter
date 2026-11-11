// Package flash writes firmware to the Arduino Mega 2560 over its USB serial port, speaking the STK500v2 protocol
// of its bootloader (what avrdude calls "wiring"), so neither avrdude nor PlatformIO is needed on the machine.
//
// The bootloader runs for about a second after a reset (a DTR pulse). It erases one page per write, counting up
// from address 0 regardless of the address loaded, so the image goes out as whole pages, in order, from 0. The
// bootloader section itself is never touched: a flash that fails half way leaves a board that can be flashed again.
package flash

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"go.bug.st/serial"
)

const (
	pageSize = 256               // ATmega2560 SPM page
	appSize  = 256*1024 - 8*1024 // flash below the 8 KB bootloader
)

// Signature of the ATmega2560.
var Signature = [3]byte{0x1E, 0x98, 0x01}

// ParseHex reads an Intel HEX file into a flat image starting at address 0 (gaps are 0xFF, the erased value).
func ParseHex(data []byte) ([]byte, error) {
	img := make([]byte, 0, 128*1024)
	var base uint32
	sc := bufio.NewScanner(bytes.NewReader(data))
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if line[0] != ':' {
			return nil, fmt.Errorf("hex line %d: no ':'", n)
		}
		b, err := hex.DecodeString(line[1:])
		if err != nil || len(b) < 5 || len(b) != int(b[0])+5 {
			return nil, fmt.Errorf("hex line %d: malformed", n)
		}
		var sum byte
		for _, x := range b {
			sum += x
		}
		if sum != 0 {
			return nil, fmt.Errorf("hex line %d: bad checksum", n)
		}
		addr, data := uint32(b[1])<<8|uint32(b[2]), b[4:len(b)-1]
		switch b[3] {
		case 0x00:
			at := base + addr
			if int(at)+len(data) > appSize {
				return nil, fmt.Errorf("hex line %d: address %#x is past the application area", n, at)
			}
			for len(img) < int(at)+len(data) {
				img = append(img, 0xFF)
			}
			copy(img[at:], data)
		case 0x01:
			if len(img) == 0 {
				return nil, errors.New("hex: no data")
			}
			return img, nil
		case 0x02:
			if len(data) == 2 {
				base = (uint32(data[0])<<8 | uint32(data[1])) << 4
			}
		case 0x04:
			if len(data) == 2 {
				base = (uint32(data[0])<<8 | uint32(data[1])) << 16
			}
		}
	}
	return nil, errors.New("hex: no end-of-file record")
}

// ---- STK500v2 ----

const (
	msgStart = 0x1B
	token    = 0x0E
	statusOK = 0x00

	cmdSignOn        = 0x01
	cmdLoadAddress   = 0x06
	cmdEnterProgmode = 0x10
	cmdLeaveProgmode = 0x11
	cmdProgramFlash  = 0x13
	cmdReadFlash     = 0x14
	cmdReadSignature = 0x1B
)

type conn struct {
	p   serial.Port
	buf []byte // read, not yet used
	seq byte
}

// byte is the next byte from the port, or an error at the deadline. (Not bufio: on a port with a read timeout it
// retries an empty read a hundred times, which outlasts the bootloader's patience.)
func (c *conn) byte(deadline time.Time) (byte, error) {
	for len(c.buf) == 0 {
		if time.Now().After(deadline) {
			return 0, errors.New("no answer from the bootloader")
		}
		b := make([]byte, 512)
		n, err := c.p.Read(b)
		if err != nil {
			return 0, err
		}
		c.buf = append(c.buf, b[:n]...)
	}
	b := c.buf[0]
	c.buf = c.buf[1:]
	return b, nil
}

func (c *conn) drain() {
	c.p.ResetInputBuffer()
	c.buf = nil
}

func (c *conn) send(body []byte, timeout time.Duration) ([]byte, error) {
	msg := []byte{msgStart, c.seq, byte(len(body) >> 8), byte(len(body)), token}
	msg = append(msg, body...)
	var sum byte
	for _, b := range msg {
		sum ^= b
	}
	if _, err := c.p.Write(append(msg, sum)); err != nil {
		return nil, err
	}
	deadline := time.Now().Add(timeout)
	next := func() (byte, error) { return c.byte(deadline) }
	for { // resync on the start byte
		b, err := next()
		if err != nil {
			return nil, err
		}
		if b != msgStart {
			continue
		}
		hdr := []byte{msgStart}
		for i := 0; i < 4; i++ {
			if b, err = next(); err != nil {
				return nil, err
			}
			hdr = append(hdr, b)
		}
		if hdr[4] != token || hdr[1] != c.seq {
			continue
		}
		n := int(hdr[2])<<8 | int(hdr[3])
		ans := make([]byte, n)
		for i := range ans {
			if ans[i], err = next(); err != nil {
				return nil, err
			}
		}
		s, err := next()
		if err != nil {
			return nil, err
		}
		for _, b := range append(hdr, ans...) {
			s ^= b
		}
		if s != 0 {
			return nil, errors.New("bad checksum from the bootloader")
		}
		c.seq++
		if len(ans) < 2 || ans[0] != body[0] || ans[1] != statusOK {
			return nil, fmt.Errorf("bootloader refused command %#x: % x", body[0], ans)
		}
		return ans, nil
	}
}

// Progress is told what is going on: a phase and how far it is (0..1).
type Progress func(phase string, done float64)

// Flash resets the board on port into its bootloader, writes img, reads it back, and starts it.
func Flash(port string, img []byte, progress Progress) error {
	if progress == nil {
		progress = func(string, float64) {}
	}
	if len(img) == 0 || len(img) > appSize {
		return fmt.Errorf("image of %d bytes does not fit the application area", len(img))
	}
	p, err := serial.Open(port, &serial.Mode{BaudRate: 115200})
	if err != nil {
		return err
	}
	defer p.Close()
	p.SetReadTimeout(20 * time.Millisecond)
	c := &conn{p: p}

	// reset: DTR/RTS low, then high; the auto-reset capacitor turns the edge into a reset pulse
	progress("reset", 0)
	p.SetDTR(false)
	p.SetRTS(false)
	time.Sleep(250 * time.Millisecond)
	p.SetDTR(true)
	p.SetRTS(true)
	time.Sleep(50 * time.Millisecond)
	c.drain()

	var ans []byte
	for i := 0; i < 25; i++ { // the bootloader listens for about a second
		if ans, err = c.send([]byte{cmdSignOn}, 200*time.Millisecond); err == nil {
			break
		}
		c.drain()
	}
	if err != nil {
		return fmt.Errorf("the board did not enter its bootloader: %w", err)
	}
	if !bytes.Contains(ans, []byte("AVRISP_2")) && !bytes.Contains(ans, []byte("STK500_2")) {
		return fmt.Errorf("not an STK500v2 bootloader: %q", ans)
	}
	for i := byte(0); i < 3; i++ {
		ans, err := c.send([]byte{cmdReadSignature, 0x04, 0x30, 0x00, i, 0x00}, time.Second)
		if err != nil {
			return err
		}
		if len(ans) < 3 || ans[2] != Signature[i] {
			return fmt.Errorf("signature byte %d is %#x, not an ATmega2560 (%#x)", i, ans[2], Signature[i])
		}
	}
	if _, err := c.send([]byte{cmdEnterProgmode, 200, 100, 25, 32, 0, 0x53, 3, 0xAC, 0x53, 0x00, 0x00}, time.Second); err != nil {
		return err
	}
	leave := func() { c.send([]byte{cmdLeaveProgmode, 1, 1}, time.Second) }

	pages := (len(img) + pageSize - 1) / pageSize
	padded := make([]byte, pages*pageSize)
	for i := range padded {
		padded[i] = 0xFF
	}
	copy(padded, img)

	load := func() error {
		_, err := c.send([]byte{cmdLoadAddress, 0x80, 0, 0, 0}, time.Second) // word address 0; bit 31: extended (>64K words)
		return err
	}
	if err := load(); err != nil {
		leave()
		return err
	}
	for i := 0; i < pages; i++ {
		body := append([]byte{cmdProgramFlash, pageSize >> 8, pageSize & 0xFF, 0xC1, 10, 0x40, 0x4C, 0x20, 0x00, 0x00}, padded[i*pageSize:(i+1)*pageSize]...)
		if _, err := c.send(body, 2*time.Second); err != nil {
			leave()
			return fmt.Errorf("writing page %d of %d: %w", i+1, pages, err)
		}
		progress("write", float64(i+1)/float64(pages))
	}
	if err := load(); err != nil {
		leave()
		return err
	}
	for i := 0; i < pages; i++ {
		ans, err := c.send([]byte{cmdReadFlash, pageSize >> 8, pageSize & 0xFF, 0x20}, 2*time.Second)
		if err != nil {
			leave()
			return fmt.Errorf("reading page %d back: %w", i+1, err)
		}
		if len(ans) < 2+pageSize || !bytes.Equal(ans[2:2+pageSize], padded[i*pageSize:(i+1)*pageSize]) {
			leave()
			return fmt.Errorf("page %d reads back different from what was written", i+1)
		}
		progress("verify", float64(i+1)/float64(pages))
	}
	leave() // the bootloader starts the new firmware
	progress("done", 1)
	return nil
}

// Size says how much of the application area img takes, for messages.
func Size(img []byte) string {
	return strconv.Itoa(len(img)) + " bytes (" + strconv.Itoa(len(img)*100/appSize) + "% of flash)"
}
