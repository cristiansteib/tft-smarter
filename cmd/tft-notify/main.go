// tft-notify shows a notice on the TFT desk screen driven by tft-monitor: something to know (info, success, warn,
// error) or something the user has to do now (action). Made to be easy for people and for agents: plain flags,
// --json output, --help-json for discovery, and `agents-install` to teach coding agents about the screen.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/cristiansteib/tft-smarter/internal/notify"
	"github.com/cristiansteib/tft-smarter/internal/version"
)

// Exit codes.
const (
	exitOK       = 0
	exitErr      = 1
	exitUsage    = 2
	exitDown     = 3 // tft-monitor is not running
	exitExpired  = 4 // --wait: the notice expired before anyone tapped it
	exitTimedOut = 5 // --wait: gave up waiting (the notice is still up)
	exitMismatch = 6 // status: the board's firmware and tft-monitor do not fit together
)

var exitCodes = map[string]string{
	"0": "ok (with --wait: the user dismissed it)", "1": "error", "2": "bad usage",
	"3": "tft-monitor is not running (no screen): carry on without it",
	"4": "--wait: the notice expired before the user tapped it", "5": "--wait: --timeout reached, the notice is still up",
	"6": "status: firmware and tft-monitor do not fit together",
}

// ---- flags, declared once: they drive parsing, --help and --help-json ----

type opt struct {
	Name     string   `json:"name"`
	Short    string   `json:"short,omitempty"`
	Type     string   `json:"type"` // bool | string | int | duration | json
	Default  string   `json:"default,omitempty"`
	Enum     []string `json:"enum,omitempty"`
	Repeat   bool     `json:"repeatable,omitempty"`
	Help     string   `json:"help"`
	Arg      string   `json:"-"`
	internal bool
}

type command struct {
	Name    string `json:"name"`
	Usage   string `json:"usage"`
	Summary string `json:"summary"`
	Flags   []opt  `json:"flags"`
	run     func(a *args) error
}

var jsonOpt = opt{Name: "json", Type: "bool", Help: "machine-readable output on stdout (errors too: {\"ok\":false,...})"}

func levelEnum() []string {
	n := make([]string, len(notify.Levels))
	for i, l := range notify.Levels {
		n[i] = l.Name
	}
	return n
}

var sendFlags = []opt{
	{Name: "level", Short: "l", Type: "string", Default: "info", Enum: levelEnum(), Arg: "LEVEL", Help: "how urgent: info, success, warn, error, action (action = the user must act now; blinks, stays until tapped)"},
	{Name: "body", Short: "b", Type: "string", Arg: "TEXT", Help: "longer text under the title (default: the arguments after the title; '-' reads stdin)"},
	{Name: "action", Short: "a", Type: "string", Arg: "TEXT", Help: "what the user should do, shown highlighted"},
	{Name: "source", Short: "s", Type: "string", Arg: "NAME", Help: "who sends it, shown in the header (default $TFT_NOTIFY_SOURCE)"},
	{Name: "field", Short: "f", Type: "string", Repeat: true, Arg: "KEY=VALUE", Help: "a label/value row (up to 6)"},
	{Name: "progress", Short: "p", Type: "int", Arg: "0-100", Help: "progress bar"},
	{Name: "color", Short: "c", Type: "string", Arg: "COLOR", Help: "accent colour: #RRGGBB, #RGB or a name (red orange amber yellow green lime cyan blue purple pink magenta white grey)"},
	{Name: "view", Type: "string", Default: "auto", Enum: notify.Views, Arg: "VIEW", Help: "simple = big title; detail = title, body, action, progress, fields; auto picks"},
	{Name: "ttl", Short: "t", Type: "duration", Arg: "DUR", Help: "how long it stays: 30, 90s, 5m, 1h; 0 = until tapped (default per level: info 20s, success 30s, warn 2m, error 10m, action until tapped)"},
	{Name: "id", Type: "string", Arg: "ID", Help: "update / re-show the notice with this id instead of adding one (progress, retries)"},
	{Name: "wait", Short: "w", Type: "bool", Help: "block until the user taps it away (exit 0), it expires (4) or --timeout (5)"},
	{Name: "timeout", Type: "duration", Default: "0", Arg: "DUR", Help: "with --wait: give up after this long (0 = no limit)"},
	{Name: "message", Short: "m", Type: "json", Arg: "JSON", Help: "the whole message as JSON ('-' reads stdin); flags override its fields. Keys: id level title body source action progress fields color view ttl"},
	jsonOpt,
}

var commands []*command

func init() {
	commands = []*command{
		{Name: "send", Usage: "tft-notify [send] [flags] TITLE [BODY...]", Summary: "show a notice (the default command)", Flags: sendFlags, run: cmdSend},
		{Name: "list", Usage: "tft-notify list [--all]", Summary: "the notices on screen (with --all, also the recently closed ones)",
			Flags: []opt{{Name: "all", Type: "bool", Help: "include recently dismissed / expired ones"}, jsonOpt}, run: cmdList},
		{Name: "get", Usage: "tft-notify get ID", Summary: "one notice and its state: active, dismissed or expired", Flags: []opt{jsonOpt}, run: cmdGet},
		{Name: "wait", Usage: "tft-notify wait ID [--timeout DUR]", Summary: "block until the notice is dismissed (exit 0), expires (4) or the timeout (5)",
			Flags: []opt{{Name: "timeout", Type: "duration", Default: "0", Arg: "DUR", Help: "give up after this long (0 = no limit)"}, jsonOpt}, run: cmdWait},
		{Name: "dismiss", Usage: "tft-notify dismiss ID... | --all", Summary: "take notices off the screen",
			Flags: []opt{{Name: "all", Type: "bool", Help: "dismiss every notice"}, jsonOpt}, run: cmdDismiss},
		{Name: "levels", Usage: "tft-notify levels", Summary: "the levels with their colours and default times", Flags: []opt{jsonOpt}, run: cmdLevels},
		{Name: "agents-install", Usage: "tft-notify agents-install [--dir DIR]... [--print] [--remove]",
			Summary: "install the tft-notify skill for coding agents (~/.claude/skills, and ~/.agents/skills when ~/.agents exists)",
			Flags: []opt{
				{Name: "dir", Type: "string", Repeat: true, Arg: "DIR", Help: "skills directory to install into instead of the defaults"},
				{Name: "print", Type: "bool", Help: "print the skill instead of installing it"},
				{Name: "remove", Type: "bool", Help: "uninstall it"},
				{Name: "refresh", Type: "bool", Help: "only rewrite it where it is already installed (after an update)"},
				jsonOpt}, run: cmdAgentsInstall},
		{Name: "status", Usage: "tft-notify status [--check]", Summary: "versions of tft-notify, tft-monitor and the board's firmware, and whether they fit (exit 6 if not)",
			Flags: []opt{{Name: "check", Type: "bool", Help: "also look for a newer release now"}, jsonOpt}, run: cmdStatus},
		{Name: "self-update", Usage: "tft-notify self-update [--check]", Summary: "update tft-notify (and tft-monitor) to the latest release",
			Flags: []opt{{Name: "check", Type: "bool", Help: "only say whether one is out"}, jsonOpt}, run: cmdSelfUpdate},
		{Name: "version", Usage: "tft-notify version", Summary: "this build's version", Flags: []opt{jsonOpt}, run: cmdVersion},
	}
}

// args is a parsed command line.
type args struct {
	cmd  *command
	vals map[string][]string
	pos  []string
	json bool
}

func (a *args) str(n string) string {
	if v := a.vals[n]; len(v) > 0 {
		return v[len(v)-1]
	}
	return ""
}
func (a *args) has(n string) bool  { _, ok := a.vals[n]; return ok }
func (a *args) flag(n string) bool { return a.str(n) == "true" }

type usageErr struct{ msg string }

func (e usageErr) Error() string { return e.msg }

func find(name string) *command {
	for _, c := range commands {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// parse accepts flags anywhere (--name value, --name=value, -n value), and "--" to end them.
func parse(argv []string) (*args, error) {
	a := &args{cmd: commands[0], vals: map[string][]string{}}
	if len(argv) > 0 {
		if c := find(argv[0]); c != nil {
			a.cmd, argv = c, argv[1:]
		}
	}
	for i := 0; i < len(argv); i++ {
		s := argv[i]
		if s == "--" {
			a.pos = append(a.pos, argv[i+1:]...)
			break
		}
		if len(s) < 2 || s[0] != '-' || s == "-" {
			a.pos = append(a.pos, s)
			continue
		}
		name, val, hasVal := strings.Cut(strings.TrimLeft(s, "-"), "=")
		var o *opt
		for k := range a.cmd.Flags {
			f := &a.cmd.Flags[k]
			if f.Name == name || (f.Short != "" && f.Short == name && !strings.HasPrefix(s, "--")) {
				o = f
			}
		}
		if o == nil {
			return a, usageErr{fmt.Sprintf("unknown flag %s for %q (see tft-notify %s --help)", s, a.cmd.Name, a.cmd.Name)}
		}
		if o.Type == "bool" {
			if !hasVal {
				val = "true"
			}
			b, err := strconv.ParseBool(val)
			if err != nil {
				return a, usageErr{fmt.Sprintf("--%s takes no value", o.Name)}
			}
			val = strconv.FormatBool(b)
		} else if !hasVal {
			if i+1 >= len(argv) {
				return a, usageErr{fmt.Sprintf("--%s needs a value (%s)", o.Name, o.Arg)}
			}
			i++
			val = argv[i]
		}
		if len(o.Enum) > 0 && o.Name != "level" && !slices.Contains(o.Enum, val) { // levels also take synonyms (crit, ok...): checked when sending
			return a, usageErr{fmt.Sprintf("--%s must be one of %s, not %q", o.Name, strings.Join(o.Enum, ", "), val)}
		}
		if o.Type == "int" {
			if _, err := strconv.Atoi(val); err != nil {
				return a, usageErr{fmt.Sprintf("--%s wants a number, not %q", o.Name, val)}
			}
		}
		if o.Type == "duration" {
			if _, err := parseDur(val); err != nil {
				return a, usageErr{fmt.Sprintf("--%s: %v", o.Name, err)}
			}
		}
		a.vals[o.Name] = append(a.vals[o.Name], val)
	}
	a.json = a.flag("json")
	return a, nil
}

// parseDur: "30" (seconds), "90s", "5m", "1h30m"; "0", "never" and "sticky" are 0.
func parseDur(s string) (time.Duration, error) {
	switch s {
	case "never", "sticky", "forever":
		return 0, nil
	}
	if n, err := strconv.Atoi(s); err == nil {
		if n < 0 {
			return 0, errors.New("must be >= 0")
		}
		return time.Duration(n) * time.Second, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d < 0 {
		return 0, fmt.Errorf("bad duration %q (use 30, 90s, 5m, 1h, or 0)", s)
	}
	return d, nil
}

// ---- output ----

var stdout io.Writer = os.Stdout

func emit(a *args, data map[string]any, text string) {
	if a.json {
		out := map[string]any{"ok": true}
		for k, v := range data {
			out[k] = v
		}
		b, _ := json.Marshal(out)
		fmt.Fprintln(stdout, string(b))
		return
	}
	if text != "" {
		fmt.Fprintln(stdout, text)
	}
}

type exitErrT struct {
	code int
	err  error
}

func (e exitErrT) Error() string { return e.err.Error() }

func fail(code int, err error) error { return exitErrT{code, err} }

// ---- commands ----

func cmdSend(a *args) error {
	var m notify.Message
	if j := a.str("message"); j != "" {
		b := []byte(j)
		if j == "-" {
			var err error
			if b, err = io.ReadAll(os.Stdin); err != nil {
				return err
			}
		}
		if err := json.Unmarshal(b, &m); err != nil {
			return usageErr{"--message: " + err.Error()}
		}
	}
	if len(a.pos) > 0 {
		m.Title = a.pos[0]
		if len(a.pos) > 1 && !a.has("body") {
			m.Body = strings.Join(a.pos[1:], " ")
		}
	}
	if a.has("body") {
		m.Body = a.str("body")
		if m.Body == "-" {
			b, err := io.ReadAll(os.Stdin)
			if err != nil {
				return err
			}
			m.Body = string(b)
		}
	}
	if m.Title == "" && m.Body == "" && a.str("message") == "" && piped(os.Stdin) { // `make 2>&1 | tail -3 | tft-notify -l error`
		b, _ := io.ReadAll(os.Stdin)
		m.Body = string(b)
	}
	set := func(name string, dst *string) {
		if a.has(name) {
			*dst = a.str(name)
		}
	}
	set("level", &m.Level)
	set("action", &m.Action)
	set("source", &m.Source)
	set("color", &m.Color)
	set("view", &m.View)
	set("id", &m.ID)
	if m.Source == "" {
		m.Source = os.Getenv("TFT_NOTIFY_SOURCE")
	}
	if a.has("progress") {
		p, _ := strconv.Atoi(a.str("progress"))
		m.Progress = &p
	}
	if a.has("ttl") {
		d, _ := parseDur(a.str("ttl"))
		s := int((d + time.Second - 1) / time.Second)
		m.TTL = &s
	}
	for _, f := range a.vals["field"] {
		k, v, ok := strings.Cut(f, "=")
		if !ok {
			return usageErr{fmt.Sprintf("--field wants KEY=VALUE, not %q", f)}
		}
		m.Fields = append(m.Fields, notify.Field{Label: strings.TrimSpace(k), Value: strings.TrimSpace(v)})
	}
	if _, err := m.Normalize(); err != nil { // here for a clear usage error before connecting; the daemon checks again
		return usageErr{err.Error()}
	}
	var n notify.Notice
	if err := notify.Call(notify.Request{Cmd: "notify", Message: &m}, &n); err != nil {
		return daemonErr(err)
	}
	if !a.flag("wait") {
		emit(a, map[string]any{"id": n.ID}, n.ID)
		return nil
	}
	return waitFor(a, n.ID)
}

func daemonErr(err error) error {
	if errors.Is(err, notify.ErrNotRunning) {
		return fail(exitDown, err)
	}
	return fail(exitErr, err)
}

func cmdList(a *args) error {
	var r struct{ Active, Recent []notify.Notice }
	if err := notify.Call(notify.Request{Cmd: "notices"}, &r); err != nil {
		return daemonErr(err)
	}
	data := map[string]any{"active": brief(r.Active)}
	if a.flag("all") {
		data["recent"] = brief(r.Recent)
	}
	var b strings.Builder
	line := func(n notify.Notice) {
		left := "until tapped"
		if n.Expires != nil {
			left = time.Until(*n.Expires).Round(time.Second).String() + " left"
		}
		if n.State != "active" {
			left = n.State
			if n.DismissedBy != "" {
				left += " by " + n.DismissedBy
			}
		}
		fmt.Fprintf(&b, "%-8s  %-7s  %-14s  %s\n", n.ID, n.Level, left, n.Title)
	}
	for _, n := range r.Active {
		line(n)
	}
	if len(r.Active) == 0 {
		b.WriteString("nothing on screen\n")
	}
	if a.flag("all") {
		for _, n := range r.Recent {
			line(n)
		}
	}
	emit(a, data, strings.TrimRight(b.String(), "\n"))
	return nil
}

type row struct {
	ID    string `json:"id"`
	Level string `json:"level"`
	Title string `json:"title"`
	State string `json:"state,omitempty"` // only when not active
	Left  *int   `json:"left,omitempty"`  // seconds until it expires
}

// brief: what an agent needs from a list, not the whole notice (that is `get ID`).
func brief(ns []notify.Notice) []row {
	out := []row{}
	for _, n := range ns {
		r := row{ID: n.ID, Level: n.Level, Title: n.Title}
		if n.State != "active" {
			r.State = n.State
		} else if n.Expires != nil {
			l := int(time.Until(*n.Expires).Seconds())
			r.Left = &l
		}
		out = append(out, r)
	}
	return out
}

func one(a *args) (string, error) {
	if len(a.pos) != 1 {
		return "", usageErr{fmt.Sprintf("usage: %s", a.cmd.Usage)}
	}
	return a.pos[0], nil
}

func cmdGet(a *args) error {
	id, err := one(a)
	if err != nil {
		return err
	}
	var n notify.Notice
	if err := notify.Call(notify.Request{Cmd: "notice", ID: id}, &n); err != nil {
		return daemonErr(err)
	}
	emit(a, map[string]any{"notice": n}, fmt.Sprintf("%s %s %s: %s", n.ID, n.State, n.Level, n.Title))
	return nil
}

func cmdWait(a *args) error {
	id, err := one(a)
	if err != nil {
		return err
	}
	return waitFor(a, id)
}

// waitFor polls the notice until it closes.
func waitFor(a *args, id string) error {
	limit, _ := parseDur(a.str("timeout"))
	start := time.Now()
	for {
		var n notify.Notice
		if err := notify.Call(notify.Request{Cmd: "notice", ID: id}, &n); err != nil {
			return daemonErr(err)
		}
		data := map[string]any{"id": id, "result": n.State}
		switch n.State {
		case "dismissed":
			data["dismissedBy"] = n.DismissedBy
			emit(a, data, "dismissed by "+n.DismissedBy)
			return nil
		case "expired":
			emit(a, data, "expired")
			return exitErrT{exitExpired, nil}
		}
		if limit > 0 && time.Since(start) >= limit {
			data["result"] = "timeout"
			emit(a, data, "timed out, still on screen")
			return exitErrT{exitTimedOut, nil}
		}
		time.Sleep(400 * time.Millisecond)
	}
}

func cmdDismiss(a *args) error {
	if !a.flag("all") && len(a.pos) == 0 {
		return usageErr{"usage: " + a.cmd.Usage}
	}
	var all []string
	ids := a.pos
	if a.flag("all") {
		ids = []string{""}
	}
	for _, id := range ids {
		var r struct{ Dismissed []string }
		if err := notify.Call(notify.Request{Cmd: "dismiss", ID: id, All: a.flag("all"), By: "cli"}, &r); err != nil {
			return daemonErr(err)
		}
		all = append(all, r.Dismissed...)
	}
	if all == nil {
		all = []string{}
	}
	emit(a, map[string]any{"dismissed": all}, fmt.Sprintf("dismissed %d", len(all)))
	return nil
}

func cmdLevels(a *args) error {
	var b strings.Builder
	for _, l := range notify.Levels {
		ttl := "until tapped"
		if l.TTL > 0 {
			ttl = human(l.TTL)
		}
		fmt.Fprintf(&b, "%-8s %s  %-13s %s\n", l.Name, l.Color, ttl, l.Use)
	}
	emit(a, map[string]any{"levels": notify.Levels}, strings.TrimRight(b.String(), "\n"))
	return nil
}

// human: 20 -> "20s", 120 -> "2m", 600 -> "10m".
func human(sec int) string {
	if sec >= 60 && sec%60 == 0 {
		return strconv.Itoa(sec/60) + "m"
	}
	return strconv.Itoa(sec) + "s"
}

// piped: stdin is a pipe or a file (not a terminal, not /dev/null), so there is text to read.
func piped(f *os.File) bool {
	st, err := f.Stat()
	return err == nil && (st.Mode()&os.ModeNamedPipe != 0 || st.Mode().IsRegular())
}

// ---- help ----

func helpText(c *command) string {
	var b strings.Builder
	if c == nil {
		b.WriteString("tft-notify - show a notice on the TFT desk screen (driven by tft-monitor)\n\n")
		b.WriteString("Usage:\n  tft-notify [flags] TITLE [BODY...]\n  tft-notify COMMAND [flags]\n\nCommands:\n")
		for _, c := range commands {
			fmt.Fprintf(&b, "  %-15s %s\n", c.Name, c.Summary)
		}
		b.WriteString("\nLevels:\n")
		for _, l := range notify.Levels {
			fmt.Fprintf(&b, "  %-8s %s\n", l.Name, l.Use)
		}
		c = commands[0]
		b.WriteString("\nSend flags:\n")
	} else {
		fmt.Fprintf(&b, "%s\n\nUsage:\n  %s\n\nFlags:\n", c.Summary, c.Usage)
	}
	for _, o := range c.Flags {
		n := "--" + o.Name
		if o.Short != "" {
			n = "-" + o.Short + ", " + n
		}
		if o.Arg != "" {
			n += " " + o.Arg
		}
		fmt.Fprintf(&b, "  %-24s %s\n", n, o.Help)
	}
	if c == commands[0] {
		b.WriteString(`
Examples:
  tft-notify "Coffee is ready"
  tft-notify -l success "Deploy done" "api v2.3.1 is live"
  tft-notify -l action -a "Approve in the terminal" -s claude "Push to main?"
  tft-notify -l info --id build -p 40 "Building firmware"      # same id: updates in place
  tft-notify -l action --wait --timeout 10m "Plug in the board"  # blocks until tapped
  make 2>&1 | tail -3 | tft-notify -l error                      # stdin becomes the text
  tft-notify --json -m '{"level":"warn","title":"Disk 91%","fields":{"host":"nas"}}'

Also: --help-json (everything above as JSON), status, self-update. Exit codes: 0 ok, 1 error, 2 usage,
3 tft-monitor not running, 4 expired (--wait), 5 timeout (--wait), 6 firmware/monitor mismatch (status).
`)
	}
	return b.String()
}

// helpJSON is the discovery document for agents: compact, everything needed to call the CLI and nothing else.
func helpJSON() string {
	lv := map[string]string{}
	for _, l := range notify.Levels {
		t := "until tapped"
		if l.TTL > 0 {
			t = human(l.TTL)
		}
		lv[l.Name] = l.Use + "; " + t
	}
	cmds := map[string]any{}
	for _, c := range commands {
		fl := map[string]string{}
		for _, o := range c.Flags {
			if o.Name == "json" {
				continue
			}
			k := "--" + o.Name
			if o.Short != "" {
				k = "-" + o.Short + "|" + k
			}
			if o.Arg != "" {
				k += " " + o.Arg
			}
			if o.Repeat {
				k += " (repeat)"
			}
			fl[k] = o.Help
		}
		e := map[string]any{"usage": c.Usage, "does": c.Summary}
		if len(fl) > 0 {
			e["flags"] = fl
		}
		cmds[c.Name] = e
	}
	doc := map[string]any{
		"tft-notify": "show a notice on the user's desk screen until it expires or they tap it",
		"commands":   cmds, "levels": lv,
		"json":    "--json on any command: one line, {\"ok\":true,...} or {\"ok\":false,\"error\":..,\"code\":N}; send gives {id}, --wait adds {result: dismissed|expired|timeout}",
		"exit":    exitCodes,
		"message": "-m JSON keys: id level title body source action progress fields({k:v}) color view ttl(seconds, 0=until tapped)",
	}
	b, _ := json.Marshal(doc)
	return string(b)
}

// ---- main ----

func main() { os.Exit(run(os.Args[1:])) }

func run(argv []string) int {
	wantJSON := slices.Contains(argv, "--json")
	for _, s := range argv {
		if s == "--" {
			break
		}
		switch s {
		case "--help-json":
			fmt.Fprintln(stdout, helpJSON())
			return exitOK
		case "--version", "-version":
			fmt.Fprintln(stdout, version.Get())
			return exitOK
		}
	}
	if len(argv) == 0 || (len(argv) == 1 && (argv[0] == "help")) {
		fmt.Fprint(stdout, helpText(nil))
		return exitOK
	}
	for _, s := range argv {
		if s == "--" {
			break
		}
		if s == "-h" || s == "--help" || s == "-help" {
			c := find(argv[0])
			if c == commands[0] {
				c = nil
			}
			fmt.Fprint(stdout, helpText(c))
			return exitOK
		}
	}
	a, err := parse(argv)
	if err == nil {
		done := startUpdateCheck(a)
		err = a.cmd.run(a)
		if err == nil {
			done()
		}
	}
	if err == nil {
		return exitOK
	}
	code := exitErr
	var ue usageErr
	var ee exitErrT
	switch {
	case errors.As(err, &ue):
		code = exitUsage
	case errors.As(err, &ee):
		code = ee.code
		if ee.err == nil { // a result, already printed
			return code
		}
		err = ee.err
	}
	if wantJSON {
		b, _ := json.Marshal(map[string]any{"ok": false, "error": err.Error(), "code": code})
		fmt.Fprintln(stdout, string(b))
	} else {
		fmt.Fprintln(os.Stderr, "tft-notify:", err)
	}
	return code
}
