package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/cristiansteib/tft-smarter/internal/notify"
	"github.com/cristiansteib/tft-smarter/internal/selfupdate"
	"github.com/cristiansteib/tft-smarter/internal/version"
)

// ---- which firmware is on the board, and whether this daemon can drive it ----
//
// The firmware says "V <proto> <version>" at boot and every 5 s. Firmware from before that line existed says
// only "ok=N bad=M" every 5 s: hearing that without a V line is firmware too old to have a protocol number (0).

type boardDoc struct {
	Connected  bool   `json:"connected"`
	Proto      int    `json:"proto"` // 0: from before protocol numbers
	Firmware   string `json:"firmware,omitempty"`
	Compatible *bool  `json:"compatible,omitempty"` // unset until the board has said what it is
	Problem    string `json:"problem,omitempty"`
}

var board struct {
	sync.Mutex
	connected, heard, versioned bool
	since                       time.Time
	proto                       int
	fw                          string
	warned                      string // the problem a notice was already shown for
}

func boardUp(now time.Time) {
	board.Lock()
	defer board.Unlock()
	board.connected, board.heard, board.versioned, board.since, board.proto, board.fw = true, false, false, now, 0, ""
}

func boardDown() {
	board.Lock()
	board.connected = false
	board.Unlock()
}

// boardLine reads what the board printed.
func boardLine(line string) {
	f := strings.Fields(line)
	board.Lock()
	defer board.Unlock()
	switch {
	case len(f) >= 2 && f[0] == "V":
		if p, err := strconv.Atoi(f[1]); err == nil {
			board.versioned, board.proto = true, p
			if len(f) > 2 {
				board.fw = f[2]
			}
		}
	case strings.HasPrefix(line, "ok="):
		board.heard = true
	}
}

func boardState() boardDoc {
	board.Lock()
	defer board.Unlock()
	d := boardDoc{Connected: board.connected, Proto: board.proto, Firmware: board.fw}
	switch {
	case !board.connected:
		d.Problem = "the board is not connected"
		return d
	case !board.versioned && !board.heard && time.Since(board.since) < 12*time.Second:
		return d // it has not said anything yet
	}
	ok, why := version.Fits(d.Proto, version.MinProto, version.Proto)
	d.Compatible, d.Problem = &ok, why
	return d
}

// boardCheck runs on every tick: a mismatch is logged and shown on the screen once per firmware, and firmware
// newer than this daemon makes it look for an update right away.
func boardCheck() {
	d := boardState()
	if d.Compatible == nil {
		return
	}
	if *d.Compatible { // fits (again): the warning has done its job
		board.Lock()
		warned := board.warned != ""
		board.warned = ""
		board.Unlock()
		if warned {
			notes.dismiss("tft-compat", false, "tft-monitor", time.Now())
		}
		return
	}
	key := fmt.Sprintf("%d %s", d.Proto, d.Firmware)
	board.Lock()
	seen := board.warned == key
	board.warned = key
	board.Unlock()
	if seen {
		return
	}
	fw := d.Firmware
	if fw == "" {
		fw = "unversioned"
	}
	log.Printf("firmware %s (protocol %d) does not fit tft-monitor %s (protocols %d..%d): %s",
		fw, d.Proto, version.Get(), version.MinProto, version.Proto, d.Problem)
	ttl := 600
	notes.add(notify.Message{ID: "tft-compat", Level: "warn", Source: "tft-monitor", TTL: &ttl,
		Title:  "Screen and monitor do not match",
		Body:   fmt.Sprintf("Firmware %s speaks protocol %d; tft-monitor %s speaks %d..%d.", fw, d.Proto, version.Get(), version.MinProto, version.Proto),
		Fields: notify.Fields{{Label: "fix", Value: map[bool]string{true: "flash the firmware", false: "tft-notify self-update"}[d.Proto < version.MinProto]}}},
		time.Now())
	if d.Proto > version.Proto {
		kickUpdate()
	}
}

// ---- updating itself: a released tft-monitor keeps itself on the latest release ----

var (
	updateKick = make(chan struct{}, 1)
	updates    struct {
		sync.Mutex
		on     bool
		last   selfupdate.Record
		status string // what the last attempt did
	}
)

type updateDoc struct {
	Auto      bool       `json:"auto"`
	Latest    string     `json:"latest,omitempty"`
	CheckedAt *time.Time `json:"checkedAt,omitempty"`
	Status    string     `json:"status,omitempty"`
}

func updateState() updateDoc {
	updates.Lock()
	defer updates.Unlock()
	d := updateDoc{Auto: updates.on, Latest: updates.last.Latest, Status: updates.status}
	if t := updates.last.CheckedAt; !t.IsZero() {
		d.CheckedAt = &t
	}
	return d
}

func kickUpdate() {
	select {
	case updateKick <- struct{}{}:
	default:
	}
}

// autoUpdate looks for a release a couple of minutes after start, then every few hours (or when kicked), and
// replaces this binary and restarts in place when one is out that can drive the board.
func autoUpdate(on bool) {
	if !on || !selfupdate.Enabled() {
		return
	}
	updates.Lock()
	updates.on = true
	updates.Unlock()
	go func() {
		t := time.NewTimer(2 * time.Minute)
		for {
			select {
			case <-t.C:
			case <-updateKick:
				t.Stop()
			}
			if to, err := updateSelf(); err != nil {
				log.Printf("self-update: %v", err)
			} else if to != "" {
				restart()
			}
			t.Reset(selfupdate.Every)
		}
	}()
}

// updateSelf installs the latest release over this binary when it is newer and fits the board. It returns the
// version installed ("" when nothing was), and does not restart.
func updateSelf() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	say := func(s string) {
		updates.Lock()
		updates.status = s
		updates.Unlock()
	}
	rel, err := selfupdate.Latest(ctx)
	rec := selfupdate.Record{CheckedAt: time.Now(), Latest: rel.Version}
	if err != nil {
		rec = selfupdate.Load()
		rec.CheckedAt = time.Now()
	}
	selfupdate.Save(rec)
	updates.Lock()
	updates.last = rec
	updates.Unlock()
	if err != nil {
		say(err.Error())
		return "", err
	}
	cur := version.Get()
	if !version.Newer(rel.Version, cur) {
		say("up to date")
		return "", nil
	}
	exe, err := selfupdate.Exe()
	if err != nil {
		return "", err
	}
	if selfupdate.ByMise(exe) {
		say("v" + rel.Version + " is out; this binary is mise's: mise upgrade " + selfupdate.MiseTool)
		return "", nil
	}
	// the new daemon has to be able to drive the firmware that is on the board, or the screen would go dark
	if c, ok := selfupdate.Compatibility(ctx, rel); ok {
		if b := boardState(); b.Compatible != nil {
			if fits, _ := version.Fits(b.Proto, c.MinProto, c.Proto); !fits {
				msg := fmt.Sprintf("v%s needs firmware protocol %d..%d, the board has %d: flash the firmware, then it updates", rel.Version, c.MinProto, c.Proto, b.Proto)
				say(msg)
				return "", nil
			}
		}
	}
	sums, err := selfupdate.Sums(ctx, rel)
	if err != nil {
		say(err.Error())
		return "", err
	}
	if err := selfupdate.Install(ctx, rel, sums, "tft-monitor", exe); err != nil {
		say(err.Error())
		return "", err
	}
	say("updated " + cur + " -> " + rel.Version)
	log.Printf("self-update: installed v%s over v%s at %s", rel.Version, cur, exe)
	return rel.Version, nil
}

// restart runs the new binary in this process (same pid, so systemd sees nothing but a short reconnect). Nothing
// open may survive into it: the serial library opens the port exclusively and without close-on-exec, and the new
// process would find it busy, held by itself.
func restart() {
	exe, err := selfupdate.Exe()
	if err == nil {
		forEachFD(func(fd int, _ string) { syscall.CloseOnExec(fd) })
		err = syscall.Exec(exe, os.Args, os.Environ())
	}
	log.Printf("restart after the update failed (%v): exiting so the service restarts", err)
	os.Exit(0)
}

// forEachFD calls f with every open descriptor above stderr and what it points at.
func forEachFD(f func(fd int, target string)) {
	ents, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return
	}
	for _, e := range ents {
		fd, err := strconv.Atoi(e.Name())
		if err != nil || fd <= 2 {
			continue
		}
		t, _ := os.Readlink("/proc/self/fd/" + e.Name())
		f(fd, t)
	}
}

// closeInheritedTTYs closes serial ports an older build left open across its restart (0.16.2 did): this process
// opened none yet, so any it holds would only keep the board busy.
func closeInheritedTTYs() {
	forEachFD(func(fd int, t string) {
		if strings.HasPrefix(t, "/dev/tty") {
			syscall.Close(fd)
		}
	})
}
