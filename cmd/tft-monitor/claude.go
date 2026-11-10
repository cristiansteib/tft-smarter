package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"syscall"
	"time"
)

type claudeInfo struct {
	ctx, s5h, s5hResetMin, wk, busy int
	addSession, projWeekly, budget  int // forecast: extra session % until reset, projected weekly %, today's budget %
	sessions, busySessions          int
	// from the usage file: exact reset times, pace against the daily budget, burn rate and risk
	sessionResetAt, weeklyResetAt time.Time
	pace, rate, pOver             float64 // pace: % over (+) or under (-) today's budget; rate: % of the 5 h limit per hour
	fetchedAt                     time.Time
	status                        string
}

var (
	slugRe   = regexp.MustCompile(`[^A-Za-z0-9]`)
	oldModel = regexp.MustCompile(`claude-3|haiku-4-5|(opus|sonnet)-4-[015]\b|-20\d{6}$`)
)

func ctxWindow(model string) int {
	if oldModel.MatchString(model) {
		return 200_000
	}
	return 1_000_000
}

type session struct {
	Pid       int    `json:"pid"`
	SessionID string `json:"sessionId"`
	Cwd       string `json:"cwd"`
	Status    string `json:"status"`
	UpdatedAt int64  `json:"updatedAt"`
}

// newestSession picks the most recently active live Claude Code session.
func newestSession(home string) (best *session, live, busy int) {
	files, _ := filepath.Glob(home + "/.claude/sessions/*.json")
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var s session
		if json.Unmarshal(b, &s) != nil || s.Pid == 0 {
			continue
		}
		if err := syscall.Kill(s.Pid, 0); err != nil && err != syscall.EPERM {
			continue
		}
		live++
		if s.Status == "busy" {
			busy++
		}
		if best == nil || s.UpdatedAt > best.UpdatedAt {
			c := s
			best = &c
		}
	}
	return
}

// lastUsage reads the tail of a transcript and returns the model and tokens of the last assistant turn.
func lastUsage(path string) (string, int) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0
	}
	defer f.Close()
	const tail = 256 * 1024
	if st, err := f.Stat(); err == nil && st.Size() > tail {
		f.Seek(st.Size()-tail, io.SeekStart)
	}
	data, _ := io.ReadAll(f)
	lines := bytes.Split(data, []byte("\n"))
	for i := len(lines) - 1; i >= 0; i-- {
		if !bytes.Contains(lines[i], []byte(`"usage"`)) {
			continue
		}
		var d struct {
			Type        string `json:"type"`
			IsSidechain bool   `json:"isSidechain"`
			Message     struct {
				Model string `json:"model"`
				Usage struct {
					In     int `json:"input_tokens"`
					Create int `json:"cache_creation_input_tokens"`
					Read   int `json:"cache_read_input_tokens"`
					Out    int `json:"output_tokens"`
				} `json:"usage"`
			} `json:"message"`
		}
		if json.Unmarshal(lines[i], &d) != nil || d.Type != "assistant" || d.IsSidechain || d.Message.Model == "<synthetic>" {
			continue
		}
		u := d.Message.Usage
		return d.Message.Model, u.In + u.Create + u.Read + u.Out
	}
	return "", 0
}

func claudeStats() claudeInfo {
	c := claudeInfo{ctx: 0, s5h: -1, s5hResetMin: -1, wk: -1}
	home, _ := os.UserHomeDir()
	s, live, busy := newestSession(home)
	c.sessions, c.busySessions = live, busy
	if s != nil {
		path := home + "/.claude/projects/" + slugRe.ReplaceAllString(s.Cwd, "-") + "/" + s.SessionID + ".jsonl"
		if model, used := lastUsage(path); model != "" {
			c.ctx = min(100, int(100*float64(used)/float64(ctxWindow(model))+0.5))
			if s.Status == "busy" {
				c.busy = 1
			}
		}
	}
	run := os.Getenv("XDG_RUNTIME_DIR")
	if run == "" {
		run = "/run/user/" + strconv.Itoa(os.Getuid())
	}
	// written by the user's claude-usage-indicator daemon (polls the OAuth usage API every 10 min)
	if b, err := os.ReadFile(run + "/claude-usage.json"); err == nil {
		var u struct {
			HasUsage     bool    `json:"has_usage"`
			SessionPct   float64 `json:"session_pct"`
			WeeklyPct    float64 `json:"weekly_pct"`
			SessionReset string  `json:"session_reset"`
			WeeklyReset  string  `json:"weekly_reset"`
			FetchedAt    int64   `json:"fetched_at"`
			Status       string  `json:"status"`
			Daily        struct {
				Budget float64 `json:"budget"`
				Pace   float64 `json:"pace"`
			} `json:"daily"`
			Forecast struct {
				AddSession float64 `json:"add_session"`
				ProjWeekly float64 `json:"proj_weekly"`
				Rate       float64 `json:"rate"`
				POver      float64 `json:"p_over"`
			} `json:"forecast"`
		}
		if json.Unmarshal(b, &u) == nil && u.HasUsage {
			c.s5h, c.wk = int(u.SessionPct+0.5), int(u.WeeklyPct+0.5)
			c.addSession, c.projWeekly, c.budget = int(u.Forecast.AddSession+0.5), int(u.Forecast.ProjWeekly+0.5), int(u.Daily.Budget+0.5)
			if t, err := time.Parse(time.RFC3339, u.SessionReset); err == nil {
				c.s5hResetMin = max(0, int(time.Until(t).Minutes()))
				c.sessionResetAt = t
			}
			if t, err := time.Parse(time.RFC3339, u.WeeklyReset); err == nil {
				c.weeklyResetAt = t
			}
			c.pace, c.rate, c.pOver, c.status = u.Daily.Pace, u.Forecast.Rate, u.Forecast.POver, u.Status
			if u.FetchedAt > 0 {
				c.fetchedAt = time.Unix(u.FetchedAt, 0)
			}
		}
	}
	return c
}

var dockerClient = &http.Client{
	Timeout: time.Second,
	Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", "/var/run/docker.sock")
	}},
}

// dockerRunning returns the number of running containers via the Docker socket (-1 if unavailable).
func dockerRunning() int {
	resp, err := dockerClient.Get("http://docker/containers/json")
	if err != nil {
		return -1
	}
	defer resp.Body.Close()
	var cs []json.RawMessage
	if json.NewDecoder(resp.Body).Decode(&cs) != nil {
		return -1
	}
	return len(cs)
}
