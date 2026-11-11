package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/cristiansteib/tft-smarter/internal/flash"
	"github.com/cristiansteib/tft-smarter/internal/notify"
	"github.com/cristiansteib/tft-smarter/internal/selfupdate"
	"github.com/cristiansteib/tft-smarter/internal/version"
)

// ---- flashing the board's firmware from the daemon, which owns the serial port ----
//
// A job is handed to the serial loop, which closes the port, flashes, and opens it again: the board then reports
// its new version like after any reset.

type flashJob struct {
	img  []byte
	what string // for the log: which firmware
	done chan error
}

var (
	flashCh  = make(chan *flashJob)
	pending  *flashJob // taken by the serial loop from flashCh, run once the port is closed
	flashMu  sync.Mutex
	flashSay string // progress, for `version`
)

var errFlash = errors.New("closing the port to flash the board")

// takeFlash is checked on every tick of the serial loop.
func takeFlash() bool {
	select {
	case j := <-flashCh:
		pending = j
		return true
	default:
		return false
	}
}

// runFlash flashes the pending job, if any, on the closed port; true when there was one.
func runFlash(port string) bool {
	j := pending
	if j == nil {
		return false
	}
	pending = nil
	log.Printf("flash: writing %s (%s) to %s", j.what, flash.Size(j.img), port)
	last := ""
	err := flash.Flash(port, j.img, func(phase string, done float64) {
		s := fmt.Sprintf("%s %d%%", phase, int(done*100))
		if s != last && (int(done*100)%25 == 0 || phase == "done") {
			last = s
			flashMu.Lock()
			flashSay = s
			flashMu.Unlock()
		}
	})
	flashMu.Lock()
	flashSay = ""
	flashMu.Unlock()
	if err != nil {
		log.Printf("flash: %v", err)
	} else {
		log.Printf("flash: done")
	}
	j.done <- err
	return true
}

// requestFlash asks the serial loop to flash img and waits for it.
func requestFlash(img []byte, what string) error {
	j := &flashJob{img: img, what: what, done: make(chan error, 1)}
	select {
	case flashCh <- j:
	case <-time.After(5 * time.Second):
		return errors.New("the board is not connected")
	}
	select {
	case err := <-j.done:
		return err
	case <-time.After(4 * time.Minute):
		return errors.New("flashing did not finish")
	}
}

// releaseFirmware is the firmware of this daemon's own release (what it was built and tested with).
func releaseFirmware(ctx context.Context) ([]byte, selfupdate.Compat, error) {
	v := version.Get()
	if !version.Released(v) {
		return nil, selfupdate.Compat{}, fmt.Errorf("this tft-monitor is a build from source (%s) and has no release firmware: give a .hex", v)
	}
	rel, err := selfupdate.ByTag(ctx, v)
	if err != nil {
		return nil, selfupdate.Compat{}, err
	}
	c, _ := selfupdate.Compatibility(ctx, rel)
	hex, err := selfupdate.Fetch(ctx, rel, selfupdate.FirmwareAsset)
	return hex, c, err
}

// flashFile flashes a .hex from disk, or this release's firmware when path is "".
func flashFile(path string) error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	var hex []byte
	var err error
	what := path
	if path == "" {
		what = "the firmware of v" + version.Get()
		hex, _, err = releaseFirmware(ctx)
	} else {
		hex, err = os.ReadFile(path)
	}
	if err != nil {
		return err
	}
	img, err := flash.ParseHex(hex)
	if err != nil {
		return err
	}
	return requestFlash(img, what)
}

// autoFlash keeps the board on the firmware of this release, for a released daemon that updates itself. It
// flashes when the firmware cannot be driven, or when it is an older release whose firmware source has changed
// since; never over a build from source (somebody is working on it), and at most once per firmware per run.
var autoFlashed sync.Map

func autoFlash(d boardDoc) {
	if !updateState().Auto || d.Compatible == nil || !d.Connected {
		return
	}
	fw := d.Firmware
	if fw != "" && !version.Released(fw) {
		return // a build from source: `tft-monitor flash` does it on purpose
	}
	if _, done := autoFlashed.LoadOrStore(fw, true); done {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		hex, c, err := releaseFirmware(ctx)
		if err != nil {
			if !*d.Compatible {
				log.Printf("flash: no firmware to fix the mismatch with: %v", err)
			}
			return
		}
		if !needFlash(d, c) {
			return
		}
		img, err := flash.ParseHex(hex)
		if err != nil {
			log.Printf("flash: release firmware: %v", err)
			return
		}
		from := fw
		if from == "" {
			from = "unversioned firmware"
		}
		ttl := 120
		notes.add(notify.Message{ID: "tft-flash", Level: "info", Source: "tft-monitor", TTL: &ttl,
			Title: "Updating the screen", Body: fmt.Sprintf("Firmware %s -> v%s. Back in about 30 s.", from, version.Get())}, time.Now())
		time.Sleep(2 * time.Second) // long enough to be read
		if err := requestFlash(img, "v"+version.Get()+" (automatic)"); err != nil {
			ttl := 0
			notes.add(notify.Message{ID: "tft-flash", Level: "error", Source: "tft-monitor", TTL: &ttl,
				Title: "Firmware update failed", Body: err.Error(), Action: "Run: tft-monitor flash"}, time.Now())
			return
		}
		notes.dismiss("tft-flash", false, "tft-monitor", time.Now())
	}()
}

// needFlash: the board's firmware cannot be driven (and is older: newer firmware wants a newer daemon, not a
// downgrade), or it is a release older than the one this daemon's firmware was last changed in. Never a build from
// source: somebody is working on it.
func needFlash(d boardDoc, c selfupdate.Compat) bool {
	if d.Compatible == nil || (d.Firmware != "" && !version.Released(d.Firmware)) {
		return false
	}
	if !*d.Compatible {
		return d.Proto < version.MinProto
	}
	return d.Firmware != "" && c.FirmwareSince != "" && version.Newer(c.FirmwareSince, d.Firmware)
}

// cmdFlash is `tft-monitor flash [-hex FILE]`: through the running daemon when there is one (it owns the port),
// else straight to the port.
func cmdFlash(port, hexPath string) error {
	if hexPath != "" {
		abs, err := filepath.Abs(hexPath)
		if err != nil {
			return err
		}
		hexPath = abs
	}
	err := notify.CallTimeout(notify.Request{Cmd: "flash", Path: hexPath}, nil, 5*time.Minute)
	if err == nil {
		fmt.Println("flashed (through the running tft-monitor)")
		return nil
	}
	if !errors.Is(err, notify.ErrNotRunning) || strings.Contains(err.Error(), "not allowed") {
		return err
	}
	var hex []byte
	if hexPath == "" {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		hex, _, err = releaseFirmware(ctx)
	} else {
		hex, err = os.ReadFile(hexPath)
	}
	if err != nil {
		return err
	}
	img, err := flash.ParseHex(hex)
	if err != nil {
		return err
	}
	fmt.Printf("flashing %s to %s\n", flash.Size(img), port)
	err = flash.Flash(port, img, func(phase string, done float64) {
		fmt.Printf("\r%-8s %3d%%", phase, int(done*100))
	})
	fmt.Println()
	return err
}

func flashProgress() string {
	flashMu.Lock()
	defer flashMu.Unlock()
	return flashSay
}
