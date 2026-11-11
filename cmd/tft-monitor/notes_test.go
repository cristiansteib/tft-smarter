package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/cristiansteib/tft-smarter/internal/notify"
)

func ip(v int) *int { return &v }

func TestInbox(t *testing.T) {
	b, now := &inbox{}, time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)
	if _, err := b.add(notify.Message{}, now); err == nil {
		t.Fatal("a message without a title must fail")
	}
	if _, err := b.add(notify.Message{Title: "x", Level: "loud"}, now); err == nil {
		t.Fatal("an unknown level must fail")
	}
	info, _ := b.add(notify.Message{Title: "fyi"}, now)
	if info.ID == "" || info.Level != "info" || info.Expires == nil || info.Expires.Sub(now) != 20*time.Second {
		t.Fatalf("defaults: %+v", info)
	}
	act, _ := b.add(notify.Message{Title: "need you", Level: "urgent"}, now.Add(time.Second))
	if act.Level != "action" || act.Expires != nil {
		t.Fatalf("action stays until tapped: %+v", act)
	}
	b.add(notify.Message{Title: "oops", Level: "crit", ID: "ci"}, now.Add(2*time.Second))
	a, _ := b.list(now.Add(3 * time.Second))
	if len(a) != 3 || a[0].ID != act.ID || a[1].ID != "ci" || a[2].ID != info.ID {
		t.Fatalf("most urgent first: %+v", a)
	}
	// the same id updates the notice in place (it keeps its creation time)
	u, _ := b.add(notify.Message{Title: "oops again", Level: "error", ID: "ci", Progress: ip(140)}, now.Add(4*time.Second))
	if u.Created != now.Add(2*time.Second) || *u.Progress != 100 {
		t.Fatalf("update: %+v", u)
	}
	if a, _ := b.list(now.Add(5 * time.Second)); len(a) != 3 {
		t.Fatalf("an update must not stack: %d", len(a))
	}
	// expiry and dismissal leave the notice findable, closed
	if a, rc := b.list(now.Add(25 * time.Second)); len(a) != 2 || len(rc) != 1 || rc[0].State != "expired" {
		t.Fatalf("info expires after 20 s: %+v / %+v", a, rc)
	}
	if ids := b.dismiss(act.ID, false, "touch", now.Add(26*time.Second)); len(ids) != 1 {
		t.Fatalf("dismiss: %v", ids)
	}
	if n, ok := b.get(act.ID, now.Add(27*time.Second)); !ok || n.State != "dismissed" || n.DismissedBy != "touch" || n.ClosedAt == nil {
		t.Fatalf("get after dismiss: %+v", n)
	}
	// sending a closed id again shows it again
	if n, _ := b.add(notify.Message{Title: "back", ID: act.ID, Level: "action"}, now.Add(28*time.Second)); n.State != "active" {
		t.Fatalf("reopen: %+v", n)
	}
	if _, rc := b.list(now.Add(28 * time.Second)); len(rc) != 1 {
		t.Fatalf("a reopened notice leaves the closed list: %d", len(rc))
	}
	b.dismiss("", true, "cli", now.Add(29*time.Second))
	if a, _ := b.list(now.Add(29 * time.Second)); len(a) != 0 {
		t.Fatalf("dismiss all left %d", len(a))
	}
	for i := 0; i < notify.MaxActive+5; i++ {
		b.add(notify.Message{Title: "spam", TTL: ip(0)}, now.Add(30*time.Second))
	}
	if a, _ := b.list(now.Add(30 * time.Second)); len(a) != notify.MaxActive {
		t.Fatalf("at most %d active, got %d", notify.MaxActive, len(a))
	}
}

func TestWrap(t *testing.T) {
	for _, c := range []struct {
		adv  *[95]int
		text string
		n    int
	}{
		{&advBig, "Deploy of the api service to production finished without errors", 3},
		{&advBody, "a\nb\n\nc", 5},
		{&advBody, strings.Repeat("supercalifragilistic", 6), 4},
		{&advBody, strings.Repeat("word ", 200), 2},
	} {
		lines := wrap(c.adv, c.text, noteTextW, c.n)
		if len(lines) == 0 || len(lines) > c.n {
			t.Errorf("%q: %d lines, want 1..%d", c.text, len(lines), c.n)
		}
		for _, l := range lines {
			if textW(c.adv, l) > noteTextW {
				t.Errorf("%q: line %q is %d px, wider than %d", c.text, l, textW(c.adv, l), noteTextW)
			}
		}
	}
	if l := wrap(&advBody, strings.Repeat("word ", 200), noteTextW, 2); !strings.HasSuffix(l[1], "...") {
		t.Errorf("cut text must end in ...: %q", l)
	}
	if got := ascii("Café “listo” — ¿sí? 100°… ñandú\tok"); got != `Cafe "listo" - ?si? 100o... nandu ok` {
		t.Errorf("ascii: %q", got)
	}
}

// Every row of every layout fits on the screen, and the slots are the rows 0..8 once each.
func TestNoteLayoutFits(t *testing.T) {
	now := time.Date(2026, 11, 11, 10, 0, 0, 0, time.Local)
	long := strings.Repeat("The quick brown fox jumps over the lazy dog. ", 12)
	msgs := []notify.Message{
		{Title: "Coffee"},
		{Title: "Build finished", Body: "all green", Level: "success"},
		{Title: long, Body: long, Level: "warn", View: "simple"},
		{Title: long, Body: long, Level: "action", Source: "claude-code-agent-x", Action: long, Progress: ip(42),
			Fields: notify.Fields{{Label: "branch", Value: "main"}, {Label: "commit", Value: strings.Repeat("abcdef", 20)}, {Label: strings.Repeat("label", 10), Value: "v"}, {Label: "a", Value: "b"}, {Label: "c", Value: "d"}, {Label: "e", Value: "f"}}},
		{Title: "Ünïcödé — “quotes”", Body: "naïve façade", Level: "error", View: "detail"},
	}
	for _, m := range msgs {
		ttl, err := m.Normalize()
		if err != nil {
			t.Fatal(err)
		}
		n := notify.Notice{Message: m, State: "active", Created: now, Updated: now}
		if ttl > 0 {
			e := now.Add(ttl)
			n.Expires = &e
		}
		for _, count := range []int{1, 3} {
			cs := noteCells(n, 0, count, now)
			seen := map[byte]bool{}
			for _, c := range cs {
				if c.slot > 8 || seen[c.slot] {
					t.Fatalf("%q: slot %d out of range or used twice", m.Title, c.slot)
				}
				seen[c.slot] = true
				label, val, _ := strings.Cut(c.text, "\t")
				adv := &advBody
				if c.style&sBig != 0 {
					adv = &advBig
				}
				w := textW(adv, label) + textW(&advBold, val)
				if c.style&sBar != 0 {
					w = 150 // label | fixed bar | value: the bar row has its own layout
				}
				if w > noteTextW {
					t.Errorf("%q (%s): row %d %q is %d px, wider than %d", m.Title, noteView(m), c.slot, c.text, w, noteTextW)
				}
				for i := 0; i < len(c.text); i++ {
					if b := c.text[i]; b > 126 || (b < 32 && b != '\t') {
						t.Errorf("row %d has a byte the board cannot draw: %q", c.slot, c.text)
					}
				}
			}
			if !seen[0] || !seen[8] {
				t.Errorf("%q: header and footer are always there", m.Title)
			}
		}
	}
	if v := noteView(msgs[3]); v != "detail" {
		t.Errorf("an action, progress and fields want the detail view, got %s", v)
	}
	if v := noteView(msgs[1]); v != "simple" {
		t.Errorf("a short message wants the simple view, got %s", v)
	}
}

func TestNotePage(t *testing.T) {
	r := newRig(t)
	n, err := notes.add(notify.Message{Title: "Need your OK", Level: "action", Color: "#00FF00"}, r.now)
	if err != nil {
		t.Fatal(err)
	}
	r.sent = nil
	r.run(time.Second)
	r.want("note")
	// the accent goes out before the screen switch (the board draws the strip with it), the screen in notice mode
	var order []byte
	for _, f := range r.sent {
		switch f[2] {
		case frameColor, frameScreen:
			order = append(order, f[2])
			if f[2] == frameScreen && f[4]&0x04 == 0 {
				t.Fatalf("screen frame without the notice bit: % x", f)
			}
		}
	}
	if !bytes.HasPrefix(order, []byte{frameColor, frameColor, frameScreen}) {
		t.Fatalf("frame order % x, want colour, colour, screen", order)
	}
	if !bytes.Contains(bytes.Join(r.sent, nil), colorFrame(pAccent, notify.RGB565("#00FF00"))) {
		t.Fatal("the custom colour was not sent")
	}
	// a second, less urgent notice queues behind it; a swipe shows it
	notes.add(notify.Message{Title: "FYI", ID: "fyi", TTL: ip(0)}, r.now)
	r.run(time.Second)
	if r.p.noteID != n.ID {
		t.Fatalf("the action stays in front, showing %s", r.p.noteID)
	}
	r.swipe('L')
	r.run(time.Second)
	if r.p.noteID != "fyi" {
		t.Fatalf("swipe: showing %s", r.p.noteID)
	}
	r.swipe('L') // back to the action
	// a tap right after a notice came up does nothing; later it dismisses
	r.tap(240, 150)
	if got, _ := notes.get(n.ID, r.now); got.State != "active" {
		t.Fatal("a tap within 800 ms of the notice coming up must not dismiss it")
	}
	r.run(time.Second)
	r.tap(240, 150)
	if got, _ := notes.get(n.ID, r.now); got.State != "dismissed" || got.DismissedBy != "touch" {
		t.Fatalf("tap: %+v", got)
	}
	r.run(2 * time.Second)
	if r.p.noteID != "fyi" {
		t.Fatalf("the next notice comes up, showing %s", r.p.noteID)
	}
	// the menu still works: the page you open wins until a newer notice arrives
	r.menu(1)
	r.run(time.Second)
	r.want("cpu")
	notes.add(notify.Message{Title: "new", Level: "warn"}, r.now)
	r.run(time.Second)
	r.want("note")
	// hold: dismiss everything; back to the page that was up before the notice came
	r.run(time.Second)
	r.hold()
	r.run(2 * time.Second)
	if a, _ := notes.list(r.now); len(a) != 0 {
		t.Fatalf("hold left %d notices", len(a))
	}
	r.want("cpu")
}
