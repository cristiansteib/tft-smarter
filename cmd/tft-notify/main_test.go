package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/cristiansteib/tft-smarter/internal/notify"
	"github.com/cristiansteib/tft-smarter/internal/version"
)

// the tests never reach a real daemon through the shared socket
func init() { notify.SharedSocket = "@tft-notify-test-" + strconv.Itoa(os.Getpid()) }

func capture(t *testing.T, argv ...string) (int, string) {
	t.Helper()
	var b bytes.Buffer
	stdout = &b
	defer func() { stdout = os.Stdout }()
	code := run(argv)
	return code, b.String()
}

func TestParse(t *testing.T) {
	a, err := parse([]string{"Build done", "-l", "success", "--field", "a=1", "-f=b=2", "all", "green", "--wait", "--ttl=90s"})
	if err != nil {
		t.Fatal(err)
	}
	if a.cmd.Name != "send" || a.str("level") != "success" || len(a.vals["field"]) != 2 || a.vals["field"][1] != "b=2" ||
		!a.flag("wait") || a.str("ttl") != "90s" || strings.Join(a.pos, "|") != "Build done|all|green" {
		t.Fatalf("parsed %+v", a)
	}
	for _, bad := range [][]string{{"x", "--nope"}, {"x", "--view", "huge"}, {"x", "--progress", "lots"}, {"x", "--ttl", "soon"}, {"x", "--level"}} {
		if _, err := parse(bad); err == nil {
			t.Errorf("parse(%q) must fail", bad)
		}
	}
	if a, _ := parse([]string{"--", "--not-a-flag"}); a.pos[0] != "--not-a-flag" {
		t.Errorf("-- ends the flags: %q", a.pos)
	}
}

func TestHelpJSON(t *testing.T) {
	code, out := capture(t, "--help-json")
	var doc map[string]any
	if code != 0 || json.Unmarshal([]byte(out), &doc) != nil {
		t.Fatalf("--help-json: %d %q", code, out)
	}
	for _, k := range []string{"commands", "levels", "exit", "message", "json"} {
		if doc[k] == nil {
			t.Errorf("--help-json lacks %q", k)
		}
	}
}

func TestNotRunning(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	code, out := capture(t, "--json", "hello")
	var r struct {
		OK   bool
		Code int
	}
	if code != exitDown || json.Unmarshal([]byte(out), &r) != nil || r.OK || r.Code != exitDown {
		t.Fatalf("no daemon: exit %d %q", code, out)
	}
	if code, _ := capture(t, "--json", "-l", "bogus", "x"); code != exitUsage {
		t.Fatalf("a bad level is a usage error before any connection, got %d", code)
	}
}

// fake is a control socket that records the requests and answers like the daemon.
func fake(t *testing.T, reply func(notify.Request) any) *[]notify.Request {
	dir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", dir)
	l, err := net.Listen("unix", filepath.Join(dir, "tft-monitor.ctl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	var got []notify.Request
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			line, _ := bufio.NewReader(c).ReadBytes('\n')
			var r notify.Request
			json.Unmarshal(line, &r)
			got = append(got, r)
			json.NewEncoder(c).Encode(map[string]any{"ok": true, "data": reply(r)})
			c.Close()
		}
	}()
	return &got
}

func TestSendAndWait(t *testing.T) {
	polls := 0
	got := fake(t, func(r notify.Request) any {
		n := notify.Notice{State: "active"}
		if r.Message != nil {
			n.Message = *r.Message
		}
		n.ID = "abc"
		if r.Cmd == "notice" {
			if polls++; polls >= 2 {
				n.State, n.DismissedBy = "dismissed", "touch"
			}
		}
		return n
	})
	code, out := capture(t, "--json", "-l", "action", "-a", "plug it in", "-f", "port=ACM0", "-p", "30", "--wait", "Board unplugged", "it went away")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	var r struct {
		OK          bool
		ID          string
		Result      string
		DismissedBy string
	}
	if json.Unmarshal([]byte(out), &r) != nil || !r.OK || r.ID != "abc" || r.Result != "dismissed" || r.DismissedBy != "touch" {
		t.Fatalf("output %q", out)
	}
	m := (*got)[0].Message
	if (*got)[0].Cmd != "notify" || m.Level != "action" || m.Title != "Board unplugged" || m.Body != "it went away" || m.Action != "plug it in" ||
		len(m.Fields) != 1 || m.Fields[0].Value != "ACM0" || *m.Progress != 30 {
		t.Fatalf("sent %+v", m)
	}
	// a JSON message, with a flag overriding one of its fields
	got = fake(t, func(r notify.Request) any { return notify.Notice{Message: *r.Message} })
	code, out = capture(t, "--json", "-m", `{"level":"warn","title":"Disk 91%","fields":{"host":"nas","b":"2"},"ttl":0}`, "-c", "pink")
	m = (*got)[0].Message
	if code != 0 || m.Color != "#FF4FA3" || m.Fields[0].Label != "b" || m.TTL == nil || *m.TTL != 0 || m.Level != "warn" {
		t.Fatalf("-m: %d %s %+v", code, out, m)
	}
	if strings.Contains(out, "notice") || len(out) > 40 {
		t.Fatalf("send --json must stay short for agents: %s", out)
	}
}

func TestAgentsInstall(t *testing.T) {
	d := t.TempDir()
	if code, out := capture(t, "agents-install", "--dir", d, "--json"); code != 0 || !strings.Contains(out, "installed") {
		t.Fatalf("install: %d %s", code, out)
	}
	b, err := os.ReadFile(filepath.Join(d, "tft-notify", "SKILL.md"))
	if err != nil || !bytes.HasPrefix(b, []byte("---\nname: tft-notify\n")) {
		t.Fatalf("skill file: %v %q", err, b[:min(len(b), 40)])
	}
	if code, _ := capture(t, "agents-install", "--dir", d, "--remove"); code != 0 {
		t.Fatal("remove failed")
	}
	if _, err := os.Stat(filepath.Join(d, "tft-notify")); !os.IsNotExist(err) {
		t.Fatal("remove left the skill dir")
	}
}

func TestStatus(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	compatible := false
	fake(t, func(r notify.Request) any {
		return map[string]any{"version": "0.16.0", "api": 1, "proto": 1, "minProto": 1,
			"board": map[string]any{"connected": true, "proto": 0, "compatible": compatible, "problem": "flash it"}}
	})
	code, out := capture(t, "status", "--json")
	var r struct {
		Compatible bool
		Monitor    string
		Problem    string
	}
	if code != exitMismatch || json.Unmarshal([]byte(out), &r) != nil || r.Compatible || r.Monitor != "0.16.0" || r.Problem != "flash it" {
		t.Fatalf("mismatch: %d %s", code, out)
	}
	compatible = true
	if code, out := capture(t, "status"); code != 0 || !strings.Contains(out, "protocol 0  ok") {
		t.Fatalf("fits: %d %s", code, out)
	}
}

func TestComplete(t *testing.T) {
	has := func(got []string, v string) bool {
		for _, g := range got {
			if strings.SplitN(g, "\t", 2)[0] == v {
				return true
			}
		}
		return false
	}
	if c := complete([]string{"st"}); !has(c, "status") || has(c, "send") {
		t.Errorf("commands: %q", c)
	}
	if c := complete([]string{"-l", ""}); !has(c, "action") || !has(c, "info") {
		t.Errorf("levels: %q", c)
	}
	if c := complete([]string{"Title", "--"}); !has(c, "--level") || !has(c, "--wait") {
		t.Errorf("send flags: %q", c)
	}
	if c := complete([]string{"list", "--"}); !has(c, "--all") || has(c, "--level") {
		t.Errorf("list flags: %q", c)
	}
	if c := complete([]string{"--color", "pi"}); len(c) != 1 || !has(c, "pink") {
		t.Errorf("colours: %q", c)
	}
	if c := complete([]string{"completion", ""}); !has(c, "zsh") {
		t.Errorf("completion shells: %q", c)
	}
	fake(t, func(notify.Request) any {
		return map[string]any{"active": []notify.Notice{{Message: notify.Message{ID: "ab12", Level: "warn", Title: "Disk"}}}}
	})
	if c := complete([]string{"dismiss", "a"}); len(c) != 1 || c[0] != "ab12\twarn: Disk" {
		t.Errorf("ids: %q", c)
	}
}

func TestCompletionInstall(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("ZDOTDIR", "")
	os.WriteFile(filepath.Join(home, ".zshrc"), []byte("export A=1\n"), 0o644)
	for i := 0; i < 2; i++ { // twice: the second changes nothing
		if code, out := capture(t, "completion", "--shell", "zsh", "--yes"); code != 0 {
			t.Fatalf("install: %d %s", code, out)
		}
	}
	rc, _ := os.ReadFile(filepath.Join(home, ".zshrc"))
	if strings.Count(string(rc), rcBegin) != 1 || !strings.HasPrefix(string(rc), "export A=1\n") {
		t.Fatalf(".zshrc: %q", rc)
	}
	f := filepath.Join(home, ".local/share/tft-notify/completions/tft-notify.zsh")
	if installedVersion(f) != version.Get() {
		t.Fatal("the script records its version")
	}
	os.WriteFile(f, []byte(versionKey+"0.0.1\nold\n"), 0o644)
	refreshCompletions()
	if installedVersion(f) != version.Get() {
		t.Fatal("an older script is rewritten")
	}
	if code, _ := capture(t, "completion", "--shell", "zsh", "--uninstall"); code != 0 {
		t.Fatal("uninstall")
	}
	if rc, _ := os.ReadFile(filepath.Join(home, ".zshrc")); string(rc) != "export A=1\n" {
		t.Fatalf("uninstall left %q", rc)
	}
	if _, err := os.Stat(f); !os.IsNotExist(err) {
		t.Fatal("uninstall left the script")
	}
	if code, out := capture(t, "completion", "bash"); code != 0 || !strings.Contains(out, "complete -o default -F _tft_notify tft-notify") {
		t.Fatalf("print: %d %s", code, out)
	}
}
