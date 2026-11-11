// Package notify is the message model shared by the tft-monitor daemon and the tft-notify CLI: levels, the message
// an agent sends, the notice the daemon keeps, and a small client for the daemon's control socket.
package notify

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Level describes one notification level. TTL is the default lifetime in seconds; 0 means it stays until dismissed.
type Level struct {
	Name  string `json:"name"`
	Label string `json:"label"` // header text on the screen
	Color string `json:"color"` // default accent, #RRGGBB
	TTL   int    `json:"ttl"`
	Use   string `json:"use"`
}

// Levels, least to most urgent. The order is the priority on the screen.
var Levels = []Level{
	{"info", "Info", "#4AA8FF", 20, "FYI, nothing to do"},
	{"success", "Done", "#27D6B4", 30, "something finished fine"},
	{"warn", "Warning", "#FFB020", 120, "worth a look soon"},
	{"error", "Error", "#FF5560", 600, "something failed"},
	{"action", "Action needed", "#FF4FA3", 0, "the user must act now; blinks and stays until tapped"},
}

// LevelOf returns the level and its priority (index in Levels).
func LevelOf(name string) (Level, int, bool) {
	for i, l := range Levels {
		if l.Name == name {
			return l, i, true
		}
	}
	return Level{}, -1, false
}

// Views: simple is a big title (and a line of body); detail adds body, source, action, progress and fields.
var Views = []string{"auto", "simple", "detail"}

// Field is a label/value row of the detail view.
type Field struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

// Fields accepts either a JSON object ({"branch":"main"}, sorted by key) or an array of {label, value}.
type Fields []Field

func (f *Fields) UnmarshalJSON(b []byte) error {
	var arr []Field
	if err := json.Unmarshal(b, &arr); err == nil {
		*f = arr
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return errors.New(`fields: want {"label":"value",...} or [{"label":..,"value":..}]`)
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	*f = (*f)[:0]
	for _, k := range keys {
		*f = append(*f, Field{k, fmt.Sprint(m[k])})
	}
	return nil
}

// Message is what a sender asks the screen to show.
type Message struct {
	ID       string `json:"id,omitempty"`       // reuse an id to update / re-show the same notice (progress, retries)
	Level    string `json:"level,omitempty"`    // info|success|warn|error|action (default info)
	Title    string `json:"title"`              // required
	Body     string `json:"body,omitempty"`     // longer text, wrapped on screen
	Source   string `json:"source,omitempty"`   // who sends it: "claude", "ci", a repo name...
	Action   string `json:"action,omitempty"`   // what the user should do
	Progress *int   `json:"progress,omitempty"` // 0..100, drawn as a bar
	Fields   Fields `json:"fields,omitempty"`   // label/value rows
	Color    string `json:"color,omitempty"`    // accent override: #RRGGBB, #RGB or a colour name
	View     string `json:"view,omitempty"`     // auto|simple|detail
	TTL      *int   `json:"ttl,omitempty"`      // seconds on screen; 0 = until dismissed; omitted = level default
}

// Notice is a message as the daemon keeps it.
type Notice struct {
	Message
	State       string     `json:"state"` // active|dismissed|expired
	Created     time.Time  `json:"created"`
	Updated     time.Time  `json:"updated"`
	Expires     *time.Time `json:"expires,omitempty"`
	ClosedAt    *time.Time `json:"closedAt,omitempty"`
	DismissedBy string     `json:"dismissedBy,omitempty"` // touch|cli|ui
}

// Limits on what the screen can usefully show.
const (
	MaxTitle  = 120
	MaxBody   = 600
	MaxAction = 120
	MaxFields = 6
	MaxActive = 20
)

// Normalize fills defaults and validates a message. It returns the effective TTL.
func (m *Message) Normalize() (time.Duration, error) {
	m.Title, m.Body = strings.TrimSpace(m.Title), strings.TrimSpace(m.Body)
	if m.Title == "" && m.Body != "" { // a body alone is fine: its first line becomes the title
		m.Title, m.Body, _ = strings.Cut(m.Body, "\n")
		m.Title, m.Body = strings.TrimSpace(m.Title), strings.TrimSpace(m.Body)
	}
	if m.Title == "" {
		return 0, errors.New("title is required")
	}
	if m.Level == "" {
		m.Level = "info"
	}
	m.Level = strings.ToLower(m.Level)
	switch m.Level { // common synonyms
	case "crit", "critical", "fail", "failed":
		m.Level = "error"
	case "warning":
		m.Level = "warn"
	case "ok", "done":
		m.Level = "success"
	case "urgent", "input", "ask":
		m.Level = "action"
	}
	lv, _, ok := LevelOf(m.Level)
	if !ok {
		return 0, fmt.Errorf("unknown level %q (use %s)", m.Level, levelNames())
	}
	if m.View == "" {
		m.View = "auto"
	}
	if !slices.Contains(Views, m.View) {
		return 0, fmt.Errorf("unknown view %q (use auto, simple, detail)", m.View)
	}
	if m.Color != "" {
		c, err := ParseColor(m.Color)
		if err != nil {
			return 0, err
		}
		m.Color = c
	}
	if m.Progress != nil {
		p := min(max(*m.Progress, 0), 100)
		m.Progress = &p
	}
	if len(m.Fields) > MaxFields {
		m.Fields = m.Fields[:MaxFields]
	}
	m.Title, m.Body, m.Action = cut(m.Title, MaxTitle), cut(m.Body, MaxBody), cut(strings.TrimSpace(m.Action), MaxAction)
	m.Source, m.ID = cut(strings.TrimSpace(m.Source), 24), cut(strings.TrimSpace(m.ID), 64)
	ttl := lv.TTL
	if m.TTL != nil {
		if *m.TTL < 0 {
			return 0, errors.New("ttl must be >= 0 (0 = until dismissed)")
		}
		ttl = min(*m.TTL, 24*3600)
	}
	return time.Duration(ttl) * time.Second, nil
}

func levelNames() string {
	n := make([]string, len(Levels))
	for i, l := range Levels {
		n[i] = l.Name
	}
	return strings.Join(n, ", ")
}

// Accent is the colour a notice is drawn with: its own, or its level's.
func (m *Message) Accent() string {
	if m.Color != "" {
		return m.Color
	}
	if l, _, ok := LevelOf(m.Level); ok {
		return l.Color
	}
	return Levels[0].Color
}

// Colors are the names ParseColor knows besides hex.
var Colors = map[string]string{
	"red": "#FF5560", "coral": "#FF5560", "orange": "#FF8A3D", "amber": "#FFB020", "yellow": "#FFD84A",
	"green": "#27D6B4", "teal": "#27D6B4", "lime": "#9BE15D", "cyan": "#4FE6FF", "blue": "#4AA8FF",
	"purple": "#B48CFF", "violet": "#B48CFF", "pink": "#FF4FA3", "magenta": "#FF4FD8", "white": "#EAF0F8", "grey": "#8A9BB4", "gray": "#8A9BB4",
}

// ParseColor accepts #RRGGBB, RRGGBB, #RGB, a colour name or a level name, and returns #RRGGBB.
func ParseColor(s string) (string, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if c, ok := Colors[s]; ok {
		return c, nil
	}
	if l, _, ok := LevelOf(s); ok {
		return l.Color, nil
	}
	h := strings.TrimPrefix(s, "#")
	if len(h) == 3 {
		h = string([]byte{h[0], h[0], h[1], h[1], h[2], h[2]})
	}
	if _, err := strconv.ParseUint(h, 16, 32); err != nil || len(h) != 6 {
		return "", fmt.Errorf("bad color %q (use #RRGGBB, #RGB or a name: red orange amber yellow green lime cyan blue purple pink magenta white grey)", s)
	}
	return "#" + strings.ToUpper(h), nil
}

// RGB565 converts #RRGGBB to the display's 16-bit colour.
func RGB565(hex string) uint16 {
	v, _ := strconv.ParseUint(strings.TrimPrefix(hex, "#"), 16, 32)
	r, g, b := uint16(v>>16&0xFF), uint16(v>>8&0xFF), uint16(v&0xFF)
	return r>>3<<11 | g>>2<<5 | b>>3
}

// Dim is the colour scaled towards black (f = 0..1), for the blinking header of "action".
func Dim(hex string, f float64) string {
	v, _ := strconv.ParseUint(strings.TrimPrefix(hex, "#"), 16, 32)
	sc := func(c uint64) uint64 { return uint64(float64(c) * f) }
	return fmt.Sprintf("#%02X%02X%02X", sc(v>>16&0xFF), sc(v>>8&0xFF), sc(v&0xFF))
}

func cut(s string, n int) string {
	if len(s) > n {
		return strings.TrimSpace(s[:n])
	}
	return s
}

// ---- client for the daemon's control socket ----

// Socket is the daemon's control socket (newline-delimited JSON).
func Socket() string {
	d := os.Getenv("XDG_RUNTIME_DIR")
	if d == "" {
		d = "/run/user/" + strconv.Itoa(os.Getuid())
	}
	return d + "/tft-monitor.ctl"
}

// SharedSocket is where the daemon also takes notices from the machine's other users: an abstract socket (Linux),
// so it needs no file another user could not open.
var SharedSocket = "@tft-monitor"

// ErrNotRunning means the daemon's socket is not there or refuses connections.
var ErrNotRunning = errors.New("tft-monitor is not running")

// Request is one control-socket command.
type Request struct {
	Cmd     string   `json:"cmd"` // notify | notices | notice | dismiss | version | update
	Message *Message `json:"message,omitempty"`
	ID      string   `json:"id,omitempty"`
	All     bool     `json:"all,omitempty"`
	By      string   `json:"by,omitempty"`
}

// Call sends one request and decodes the reply's data into out (which may be nil).
func Call(req Request, out any) error {
	c, err := net.DialTimeout("unix", Socket(), 2*time.Second)
	if err != nil { // not this user's daemon: another user's, shared with everyone
		c, err = net.DialTimeout("unix", SharedSocket, 2*time.Second)
	}
	if err != nil {
		return fmt.Errorf("%w (no socket at %s or %s): start it with `tft-monitor` or `systemctl --user start tft-monitor`", ErrNotRunning, Socket(), SharedSocket)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(5 * time.Second))
	if err := json.NewEncoder(c).Encode(req); err != nil {
		return err
	}
	line, err := bufio.NewReader(c).ReadBytes('\n')
	if err != nil {
		return fmt.Errorf("no reply from tft-monitor: %w", err)
	}
	var r struct {
		OK    bool            `json:"ok"`
		Error string          `json:"error"`
		Data  json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(line, &r); err != nil {
		return err
	}
	if !r.OK {
		if r.Error == "unknown cmd" {
			return errors.New("the running tft-monitor is too old for this command: upgrade it and restart the service")
		}
		return errors.New(r.Error)
	}
	if out != nil && len(r.Data) > 0 {
		return json.Unmarshal(r.Data, out)
	}
	return nil
}
