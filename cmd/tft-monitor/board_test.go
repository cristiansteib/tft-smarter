package main

import (
	"testing"
	"time"

	"github.com/cristiansteib/tft-smarter/internal/selfupdate"
	"github.com/cristiansteib/tft-smarter/internal/version"
)

func TestBoardCompat(t *testing.T) {
	notes = &inbox{}
	boardDown()
	if d := boardState(); d.Connected || d.Compatible != nil {
		t.Fatalf("no board: %+v", d)
	}
	boardUp(time.Now())
	if d := boardState(); d.Compatible != nil {
		t.Fatalf("a board that said nothing yet has no verdict: %+v", d)
	}
	boardLine("ok=12 bad=0\r\n") // firmware from before protocol numbers
	if d := boardState(); d.Compatible == nil || *d.Compatible || d.Problem == "" {
		t.Fatalf("unversioned firmware is too old: %+v", d)
	}
	boardCheck()
	boardCheck()
	if a, _ := notes.list(time.Now()); len(a) != 1 || a[0].ID != "tft-compat" {
		t.Fatalf("a mismatch shows one notice: %+v", a)
	}
	boardLine("V 2 v0.16.0-3-gabc\r\n")
	if d := boardState(); d.Compatible == nil || !*d.Compatible || d.Firmware != "v0.16.0-3-gabc" || d.Proto != version.Proto {
		t.Fatalf("current firmware fits: %+v", d)
	}
	boardCheck()
	if a, _ := notes.list(time.Now()); len(a) != 0 {
		t.Fatalf("the warning goes once the firmware fits: %+v", a)
	}
	boardLine("V 99 v9.0.0")
	if d := boardState(); *d.Compatible {
		t.Fatalf("firmware newer than the daemon does not fit: %+v", d)
	}
	boardDown()
}

func TestNeedFlash(t *testing.T) {
	yes, no := true, false
	c := selfupdate.Compat{Proto: 1, MinProto: 1, FirmwareSince: "0.17.0"}
	for _, x := range []struct {
		d    boardDoc
		want bool
	}{
		{boardDoc{Compatible: &no, Proto: 0}, true},                                   // unversioned, too old
		{boardDoc{Compatible: &no, Proto: 9, Firmware: "v9.0.0"}, false},              // newer: update the daemon instead
		{boardDoc{Compatible: &yes, Proto: 1, Firmware: "v0.16.3"}, true},             // older release, firmware changed since
		{boardDoc{Compatible: &yes, Proto: 1, Firmware: "v0.17.0"}, false},            // already it
		{boardDoc{Compatible: &yes, Proto: 1, Firmware: "v0.17.2"}, false},            // same source, later release
		{boardDoc{Compatible: &yes, Proto: 1, Firmware: "v0.16.1-2-gb40debe"}, false}, // a build from source
		{boardDoc{Compatible: &no, Proto: 0, Firmware: "v0.16.1-dirty"}, false},       // even when it does not fit
		{boardDoc{}, false}, // not heard from yet
	} {
		if got := needFlash(x.d, c); got != x.want {
			t.Errorf("%+v: %v", x.d, got)
		}
	}
}

func TestSharedSocketCmds(t *testing.T) {
	for _, c := range []string{"set", "goto", "update", "state"} {
		if sharedCmds[c] {
			t.Errorf("another user must not be able to %q", c)
		}
	}
}
