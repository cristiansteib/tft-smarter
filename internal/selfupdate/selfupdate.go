// Package selfupdate replaces tft-notify and tft-monitor with their latest GitHub release, the way multiversectl
// does: the release carries one raw binary per program and platform (tft-notify_linux_amd64), a checksums.txt that
// every download is checked against before it is run (the release pipeline signs that list with cosign), and a
// compat.json saying which firmware protocols that release's tft-monitor drives.
package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/cristiansteib/tft-smarter/internal/version"
)

// Repo is the only place the release source is named.
const Repo = "cristiansteib/tft-smarter"

// MiseTool is how the dotfiles declare the release for mise; a binary mise installed is upgraded through it.
const MiseTool = "github:" + Repo

const (
	checksums = "checksums.txt"
	compat    = "compat.json"
	// OptOutEnv turns every look for a release off, the daemon's automatic update included.
	OptOutEnv = "TFT_NO_UPDATE_CHECK"
)

// API is GitHub's; a variable so tests can point it at a fake.
var API = "https://api.github.com"

var client = &http.Client{Timeout: 2 * time.Minute}

// Release is one published release that already carries its files.
type Release struct {
	Version string            `json:"version"` // without the "v"
	Assets  map[string]string `json:"-"`       // name -> download URL
	// Pending is a newer version whose files are still being attached: there is nothing to download yet.
	Pending string `json:"pending,omitempty"`
}

// Compat is what a release's tft-monitor drives, and which firmware it ships.
type Compat struct {
	Proto    int `json:"proto"`
	MinProto int `json:"minProto"`
	API      int `json:"api"`
	// FirmwareSince is the oldest release whose firmware is built from the same source as this one's: a board on
	// that release or later already runs this firmware, and is not flashed again for a version string.
	FirmwareSince string `json:"firmwareSince,omitempty"`
}

// FirmwareAsset is the firmware a release ships, built for the Mega 2560.
const FirmwareAsset = "firmware.hex"

// Asset is the raw binary of a program for this platform.
func Asset(program string) string {
	return fmt.Sprintf("%s_%s_%s", program, runtime.GOOS, runtime.GOARCH)
}

func get(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "tft-smarter")
	if strings.HasPrefix(url, API) {
		req.Header.Set("Accept", "application/vnd.github+json")
		if t := os.Getenv("GITHUB_TOKEN"); t != "" {
			req.Header.Set("Authorization", "Bearer "+t)
		}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		resp.Body.Close()
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return resp, nil
}

// Latest is the newest published release that already carries its files. A release is created before its
// binaries are attached, so for a minute or so the newest one is empty; checksums.txt is attached last and marks
// a finished one.
func Latest(ctx context.Context) (Release, error) {
	resp, err := get(ctx, fmt.Sprintf("%s/repos/%s/releases?per_page=10", API, Repo))
	if err != nil {
		return Release{}, fmt.Errorf("looking up the latest release: %w", err)
	}
	defer resp.Body.Close()
	var body []struct {
		Tag        string `json:"tag_name"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
		Assets     []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return Release{}, fmt.Errorf("reading the releases: %w", err)
	}
	pending := ""
	for _, r := range body {
		if r.Draft || r.Prerelease {
			continue
		}
		rel := Release{Version: strings.TrimPrefix(r.Tag, "v"), Assets: map[string]string{}}
		for _, a := range r.Assets {
			rel.Assets[a.Name] = a.URL
		}
		if _, done := rel.Assets[checksums]; done {
			rel.Pending = pending
			return rel, nil
		}
		if pending == "" {
			pending = rel.Version
		}
	}
	if pending != "" {
		return Release{}, fmt.Errorf("v%s has no binaries attached yet: it is still being published, try again shortly", pending)
	}
	return Release{}, errors.New("no release published yet")
}

// ByTag is the release of one version, with its files.
func ByTag(ctx context.Context, ver string) (Release, error) {
	resp, err := get(ctx, fmt.Sprintf("%s/repos/%s/releases/tags/v%s", API, Repo, strings.TrimPrefix(ver, "v")))
	if err != nil {
		return Release{}, fmt.Errorf("looking up release v%s: %w", ver, err)
	}
	defer resp.Body.Close()
	var r struct {
		Tag    string `json:"tag_name"`
		Assets []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return Release{}, err
	}
	rel := Release{Version: strings.TrimPrefix(r.Tag, "v"), Assets: map[string]string{}}
	for _, a := range r.Assets {
		rel.Assets[a.Name] = a.URL
	}
	return rel, nil
}

// Fetch downloads one of the release's files into memory, checked against its checksums.
func Fetch(ctx context.Context, rel Release, name string) ([]byte, error) {
	url, ok := rel.Assets[name]
	if !ok {
		return nil, fmt.Errorf("v%s has no %s", rel.Version, name)
	}
	sums, err := Sums(ctx, rel)
	if err != nil {
		return nil, err
	}
	want, ok := sums[name]
	if !ok {
		return nil, fmt.Errorf("%s is not in %s, so it cannot be checked", name, checksums)
	}
	resp, err := get(ctx, url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	h := sha256.Sum256(b)
	if got := hex.EncodeToString(h[:]); got != want {
		return nil, fmt.Errorf("%s does not match its checksum: not used", name)
	}
	return b, nil
}

// Sums is the release's list of what each of its files should hash to. A release without one gives nothing:
// "the check was missing, so it was skipped" is the problem the check exists to prevent.
func Sums(ctx context.Context, rel Release) (map[string]string, error) {
	url, ok := rel.Assets[checksums]
	if !ok {
		return nil, fmt.Errorf("v%s has no %s, so its files cannot be checked", rel.Version, checksums)
	}
	resp, err := get(ctx, url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && len(f[0]) == 64 {
			out[strings.TrimPrefix(f[1], "*")] = strings.ToLower(f[0])
		}
	}
	return out, nil
}

// Compatibility is what the release's tft-monitor drives; ok is false for a release that does not say.
func Compatibility(ctx context.Context, rel Release) (Compat, bool) {
	url, ok := rel.Assets[compat]
	if !ok {
		return Compat{}, false
	}
	resp, err := get(ctx, url)
	if err != nil {
		return Compat{}, false
	}
	defer resp.Body.Close()
	var c Compat
	return c, json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&c) == nil && c.Proto > 0
}

// Install downloads the program's binary from rel next to dest, checks it against the release's checksums, and
// only then renames it over dest: dest is never half-written, and never written from bytes that are not the
// published ones. It works while dest is running.
func Install(ctx context.Context, rel Release, sums map[string]string, program, dest string) error {
	name := Asset(program)
	url, ok := rel.Assets[name]
	if !ok {
		return fmt.Errorf("v%s has no %s", rel.Version, name)
	}
	want, ok := sums[name]
	if !ok {
		return fmt.Errorf("%s is not in %s, so it cannot be checked", name, checksums)
	}
	resp, err := get(ctx, url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	tmp, err := os.CreateTemp(filepath.Dir(dest), "."+program+"-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp, h), resp.Body); err != nil {
		tmp.Close()
		return fmt.Errorf("downloading %s: %w", name, err)
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		return fmt.Errorf("%s does not match its checksum (want %s, got %s): nothing was installed", name, want, got)
	}
	if err := os.Chmod(tmp.Name(), 0o755); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), dest)
}

// Exe is the running binary's real path.
func Exe() (string, error) {
	p, err := os.Executable()
	if err != nil {
		return "", err
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		p = r
	}
	return p, nil
}

// ByMise says the binary is one mise installed: mise keeps one directory per version, so it is upgraded through
// mise, never overwritten in place (that would leave mise believing the old version is there).
func ByMise(path string) bool { return strings.Contains(filepath.ToSlash(path), "/mise/installs/") }

// ---- the last look, kept so a command never waits on GitHub ----

// Every is how often a look is worth making.
const Every = 6 * time.Hour

// Record is what the last look found.
type Record struct {
	CheckedAt time.Time `json:"checkedAt"`
	Latest    string    `json:"latest,omitempty"`
}

func recordPath() string {
	d, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(d, "tft-smarter", "update.json")
}

// Load is the last look, if any.
func Load() Record {
	var r Record
	if p := recordPath(); p != "" {
		if b, err := os.ReadFile(p); err == nil {
			json.Unmarshal(b, &r)
		}
	}
	return r
}

// Save keeps a look (or a claim on one: CheckedAt set before it is made, so a burst of commands asks only once).
func Save(r Record) error {
	p := recordPath()
	if p == "" {
		return errors.New("no cache directory")
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	b, _ := json.Marshal(r)
	return os.WriteFile(p, b, 0o644)
}

// Stale is true when it is time to look again.
func (r Record) Stale(now time.Time) bool {
	return r.CheckedAt.IsZero() || now.Sub(r.CheckedAt) >= Every || r.CheckedAt.After(now)
}

// Check looks for the latest release and keeps what it found. A failed look is kept as a look too (with what was
// known before), or an offline machine would try on every command.
func Check(ctx context.Context, now time.Time) (Record, error) {
	prev := Load()
	rel, err := Latest(ctx)
	if err != nil {
		prev.CheckedAt = now
		Save(prev)
		return prev, err
	}
	r := Record{CheckedAt: now, Latest: rel.Version}
	Save(r)
	return r, nil
}

// Enabled is whether this build looks for releases at all: only released builds do, and never when opted out.
func Enabled() bool { return version.Released(version.Get()) && os.Getenv(OptOutEnv) == "" }
