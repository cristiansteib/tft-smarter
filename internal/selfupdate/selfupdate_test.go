package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fake serves a releases listing: the newest one still being published, the one before complete.
func fake(t *testing.T, bin []byte, sum string) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/" + Repo + "/releases":
			fmt.Fprintf(w, `[{"tag_name":"v0.18.0","assets":[]},
			  {"tag_name":"v0.17.0","assets":[{"name":%q,"browser_download_url":"%s/bin"},{"name":"checksums.txt","browser_download_url":"%s/sums"},{"name":"compat.json","browser_download_url":"%s/compat"}]}]`,
				Asset("tft-notify"), srv.URL, srv.URL, srv.URL)
		case "/bin":
			w.Write(bin)
		case "/sums":
			fmt.Fprintf(w, "%s  %s\nnot a line\n", sum, Asset("tft-notify"))
		case "/compat":
			w.Write([]byte(`{"proto":2,"minProto":1,"api":1}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	old := API
	API = srv.URL
	t.Cleanup(func() { API = old })
}

func TestInstall(t *testing.T) {
	bin := []byte("#!/bin/sh\necho new\n")
	h := sha256.Sum256(bin)
	fake(t, bin, hex.EncodeToString(h[:]))
	ctx := context.Background()
	rel, err := Latest(ctx)
	if err != nil || rel.Version != "0.17.0" || rel.Pending != "0.18.0" {
		t.Fatalf("latest: %+v %v", rel, err)
	}
	if c, ok := Compatibility(ctx, rel); !ok || c.Proto != 2 || c.MinProto != 1 {
		t.Fatalf("compat: %+v %v", c, ok)
	}
	sums, err := Sums(ctx, rel)
	if err != nil || len(sums) != 1 {
		t.Fatalf("sums: %v %v", sums, err)
	}
	dest := filepath.Join(t.TempDir(), "tft-notify")
	os.WriteFile(dest, []byte("old"), 0o755)
	if err := Install(ctx, rel, sums, "tft-notify", dest); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(dest); string(b) != string(bin) {
		t.Fatalf("installed %q", b)
	}
	// a download that does not match its checksum is never installed
	sums[Asset("tft-notify")] = strings.Repeat("0", 64)
	os.WriteFile(dest, []byte("old"), 0o755)
	if err := Install(ctx, rel, sums, "tft-notify", dest); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("a bad checksum must fail: %v", err)
	}
	if b, _ := os.ReadFile(dest); string(b) != "old" {
		t.Fatal("a bad download replaced the binary")
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(dest), ".tft-notify-*")); len(left) != 0 {
		t.Fatalf("temp files left: %v", left)
	}
	if err := Install(ctx, rel, sums, "tft-monitor", dest); err == nil {
		t.Fatal("a program the release does not carry must fail")
	}
}

func TestRecord(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	if r := Load(); !r.Stale(r.CheckedAt) {
		t.Fatal("no record is stale")
	}
	fake(t, nil, "")
	if r, err := Check(context.Background(), time.Now()); err != nil || r.Latest != "0.17.0" || Load().Latest != "0.17.0" {
		t.Fatalf("check: %+v %v", r, err)
	}
	if ByMise("/home/x/.local/bin/tft-notify") || !ByMise("/home/x/.local/share/mise/installs/github-cristiansteib-tft-smarter/0.17.0/tft-notify") {
		t.Fatal("ByMise")
	}
}
