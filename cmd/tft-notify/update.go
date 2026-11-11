package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/cristiansteib/tft-smarter/internal/notify"
	"github.com/cristiansteib/tft-smarter/internal/selfupdate"
	"github.com/cristiansteib/tft-smarter/internal/version"
)

// daemonVersion is the daemon's answer to "version" (cmd/tft-monitor versionDoc).
type daemonVersion struct {
	Version  string `json:"version"`
	API      int    `json:"api"`
	Proto    int    `json:"proto"`
	MinProto int    `json:"minProto"`
	Board    struct {
		Connected  bool   `json:"connected"`
		Proto      int    `json:"proto"`
		Firmware   string `json:"firmware,omitempty"`
		Compatible *bool  `json:"compatible,omitempty"`
		Problem    string `json:"problem,omitempty"`
	} `json:"board"`
	Update struct {
		Auto   bool   `json:"auto"`
		Latest string `json:"latest"`
		Status string `json:"status"`
	} `json:"update"`
}

func cmdVersion(a *args) error {
	emit(a, map[string]any{"version": version.Get()}, "tft-notify "+version.Get())
	return nil
}

// cmdStatus: do the three parts fit? tft-notify -> tft-monitor (control socket) -> firmware (serial protocol).
func cmdStatus(a *args) error {
	rec := selfupdate.Load()
	if a.flag("check") {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		var err error
		if rec, err = selfupdate.Check(ctx, time.Now()); err != nil {
			fmt.Fprintln(os.Stderr, "tft-notify: no release info:", err)
		}
	}
	cur := version.Get()
	data := map[string]any{"notify": cur}
	if rec.Latest != "" {
		data["latest"] = rec.Latest
	}
	var b strings.Builder
	latest := ""
	if version.Newer(rec.Latest, cur) {
		latest = "  (" + rec.Latest + " is out: tft-notify self-update)"
	}
	fmt.Fprintf(&b, "tft-notify   %s%s\n", cur, latest)

	var v daemonVersion
	if err := notify.Call(notify.Request{Cmd: "version"}, &v); err != nil {
		if errors.Is(err, notify.ErrNotRunning) {
			return fail(exitDown, err)
		}
		if strings.Contains(err.Error(), "too old") { // a daemon from before `version`: before protocol numbers too
			data["monitor"], data["compatible"], data["problem"] = "older than 0.16", false, "tft-monitor is too old to say: update it and flash the firmware"
			emit(a, data, b.String()+"tft-monitor  older than 0.16: update it (tft-notify self-update) and flash the firmware")
			return exitErrT{exitMismatch, nil}
		}
		return fail(exitErr, err)
	}
	data["monitor"], data["api"], data["protocols"] = v.Version, v.API, []int{v.MinProto, v.Proto}
	auto := "auto-update off"
	if v.Update.Auto {
		auto = "auto-update on"
		if v.Update.Status != "" {
			auto += ": " + v.Update.Status
		}
	}
	fmt.Fprintf(&b, "tft-monitor  %s  drives firmware protocol %d..%d, %s\n", v.Version, v.MinProto, v.Proto, auto)
	bd := v.Board
	data["board"] = bd
	switch {
	case !bd.Connected:
		b.WriteString("firmware     ? (the board is not connected)\n")
	case bd.Compatible == nil:
		b.WriteString("firmware     ? (waiting for the board to say)\n")
	default:
		fw := bd.Firmware
		if fw == "" {
			fw = "unversioned"
		}
		verdict := "ok"
		if !*bd.Compatible {
			verdict = "MISMATCH: " + bd.Problem
		}
		fmt.Fprintf(&b, "firmware     %s  protocol %d  %s\n", fw, bd.Proto, verdict)
	}
	if v.API < version.API {
		b.WriteString("note: tft-monitor is older than tft-notify; some commands may be missing (tft-notify self-update)\n")
	}
	ok := bd.Compatible == nil || *bd.Compatible
	data["compatible"] = ok
	if bd.Problem != "" && bd.Connected {
		data["problem"] = bd.Problem
	}
	emit(a, data, strings.TrimRight(b.String(), "\n"))
	if !ok {
		return exitErrT{exitMismatch, nil}
	}
	return nil
}

// cmdSelfUpdate replaces this binary with the latest release (through mise when mise installed it), refreshes the
// agent skill it carries, and asks the running tft-monitor to update itself too.
func cmdSelfUpdate(a *args) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cur := version.Get()
	rel, err := selfupdate.Latest(ctx)
	if err != nil {
		if a.flag("check") { // the detached look: keep that it was made
			r := selfupdate.Load()
			r.CheckedAt = time.Now()
			selfupdate.Save(r)
		}
		return err
	}
	selfupdate.Save(selfupdate.Record{CheckedAt: time.Now(), Latest: rel.Version})
	newer := version.Newer(rel.Version, cur)
	if a.flag("check") {
		msg := "up to date (" + cur + ")"
		if newer {
			msg = rel.Version + " is out (this is " + cur + "): tft-notify self-update"
		}
		emit(a, map[string]any{"current": cur, "latest": rel.Version, "newer": newer}, msg)
		return nil
	}
	if !version.Released(cur) {
		return fmt.Errorf("this tft-notify is a build from source (%s): rebuild it, or install the release with mise (see the README)", cur)
	}
	exe, err := selfupdate.Exe()
	if err != nil {
		return err
	}
	data := map[string]any{"from": cur, "to": rel.Version, "updated": false}
	var b strings.Builder
	switch {
	case !newer:
		fmt.Fprintf(&b, "tft-notify is up to date (%s)\n", cur)
	case selfupdate.ByMise(exe):
		mise, err := exec.LookPath("mise")
		if err != nil {
			return fmt.Errorf("mise installed tft-notify but is not on PATH: run `mise upgrade %s`", selfupdate.MiseTool)
		}
		c := exec.CommandContext(ctx, mise, "upgrade", "--yes", "--quiet", selfupdate.MiseTool)
		// mise keeps the list of versions for an hour: without this it would not know the release just found
		c.Env = append(os.Environ(), "MISE_FETCH_REMOTE_VERSIONS_CACHE=0s")
		c.Stdout, c.Stderr = os.Stderr, os.Stderr // stdout stays for --json
		if err := c.Run(); err != nil {
			return fmt.Errorf("mise upgrade %s: %w", selfupdate.MiseTool, err)
		}
		if out, err := exec.Command(mise, "which", "tft-notify").Output(); err == nil {
			exe = strings.TrimSpace(string(out))
		}
		// say what mise did, not what was asked of it
		out, _ := exec.Command(exe, "version").Output()
		got := strings.TrimPrefix(strings.TrimSpace(string(out)), "tft-notify ")
		if !version.Newer(got, cur) {
			return fmt.Errorf("mise did not upgrade tft-notify (still %s, %s is out): check `mise -C / config` lists %s", cur, rel.Version, selfupdate.MiseTool)
		}
		data["updated"], data["by"], data["to"] = true, "mise", got
		fmt.Fprintf(&b, "updated tft-notify %s -> %s (with mise)\n", cur, got)
	default:
		sums, err := selfupdate.Sums(ctx, rel)
		if err != nil {
			return err
		}
		if err := selfupdate.Install(ctx, rel, sums, "tft-notify", exe); err != nil {
			return err
		}
		data["updated"] = true
		fmt.Fprintf(&b, "updated tft-notify %s -> %s\n", cur, rel.Version)
	}
	if data["updated"] == true { // the new build carries its own skill text
		exec.Command(exe, "agents-install", "--refresh", "--json").Run()
	}
	// the daemon: only this user's own can be asked (the shared socket does not take "update")
	var r struct{ Updated, Status string }
	switch err := notify.Call(notify.Request{Cmd: "update"}, &r); {
	case err == nil && r.Updated != "":
		data["monitor"] = "updated to " + r.Updated
		fmt.Fprintf(&b, "tft-monitor updated to %s and restarting\n", r.Updated)
	case err == nil:
		data["monitor"] = r.Status
		fmt.Fprintf(&b, "tft-monitor: %s\n", r.Status)
	case err != nil && strings.Contains(err.Error(), "not allowed"): // another account's monitor: it keeps itself updated
		data["monitor"] = "runs under another account and updates itself"
		b.WriteString("tft-monitor runs under another account and updates itself\n")
	case !errors.Is(err, notify.ErrNotRunning):
		data["monitor"] = err.Error()
		fmt.Fprintf(&b, "tft-monitor: %v\n", err)
	}
	emit(a, data, strings.TrimRight(b.String(), "\n"))
	return nil
}

// startUpdateCheck: for a person at a terminal, say when a release is out, the way multiversectl does. It only
// reads the last look; when that is old, a detached `self-update --check` makes the next one, so no command waits
// on GitHub. Never with --json or without a terminal: agents pay nothing for it.
func startUpdateCheck(a *args) func() {
	none := func() {}
	switch a.cmd.Name {
	case "self-update", "version", "status", "agents-install":
		return none
	}
	if a.json || !selfupdate.Enabled() || !terminal(os.Stderr) {
		return none
	}
	rec, now := selfupdate.Load(), time.Now()
	if rec.Stale(now) {
		claim := rec
		claim.CheckedAt = now
		if selfupdate.Save(claim) == nil {
			if exe, err := selfupdate.Exe(); err == nil {
				c := exec.Command(exe, "self-update", "--check", "--json")
				c.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
				c.Start()
			}
		}
	}
	return func() {
		if version.Newer(rec.Latest, version.Get()) {
			fmt.Fprintf(os.Stderr, "! tft-notify %s is out (this is %s): tft-notify self-update\n", rec.Latest, version.Get())
		}
	}
}

func terminal(f *os.File) bool {
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}
