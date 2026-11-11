package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

type nameVal struct {
	name string
	v    float64
}

type dockerInfo struct {
	ok                                    bool
	total, up, unhealthy, restarting, bad int
	badNames                              []string
	top                                   []nameVal // by memory, bytes
}

const topN = 10 // processes kept per ranking (rolling credits show them all)

type topInfo struct {
	cpu []nameVal // percent of one core
	mem []nameVal // bytes
}

// bg holds data that is slow to collect; goroutines refresh it and the main loop only reads it.
type bg struct {
	mu       sync.RWMutex
	dock     dockerInfo
	top      topInfo
	latency  int // ms, -1 = failed
	latFails int
}

func (b *bg) get() (dockerInfo, topInfo, int, int) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.dock, b.top, b.latency, b.latFails
}

func startCollectors() *bg {
	b := &bg{latency: -1}
	go func() {
		for {
			d := queryDocker()
			b.mu.Lock()
			b.dock = d
			b.mu.Unlock()
			time.Sleep(5 * time.Second)
		}
	}()
	go func() {
		prev, prevT := scanProcs(), time.Now()
		for {
			time.Sleep(4 * time.Second)
			cur, now := scanProcs(), time.Now()
			dt := now.Sub(prevT).Seconds()
			var t topInfo
			for name, a := range cur {
				if p, ok := prev[name]; ok && a.ticks >= p.ticks {
					t.cpu = append(t.cpu, nameVal{name, float64(a.ticks-p.ticks) / dt})
				}
				t.mem = append(t.mem, nameVal{name, float64(a.rss)})
			}
			sort.Slice(t.cpu, func(i, j int) bool { return t.cpu[i].v > t.cpu[j].v })
			sort.Slice(t.mem, func(i, j int) bool { return t.mem[i].v > t.mem[j].v })
			t.cpu, t.mem = t.cpu[:min(len(t.cpu), topN)], t.mem[:min(len(t.mem), topN)]
			b.mu.Lock()
			b.top = t
			b.mu.Unlock()
			prev, prevT = cur, now
		}
	}()
	go func() { // TCP connect time to a public resolver as a cheap internet latency probe
		for {
			t0 := time.Now()
			c, err := net.DialTimeout("tcp", "1.1.1.1:53", 2*time.Second)
			b.mu.Lock()
			if err != nil {
				b.latency, b.latFails = -1, b.latFails+1
			} else {
				c.Close()
				b.latency, b.latFails = int(time.Since(t0).Milliseconds()), 0
			}
			b.mu.Unlock()
			time.Sleep(5 * time.Second)
		}
	}()
	return b
}

type procAgg struct{ ticks, rss uint64 }

// scanProcs sums CPU ticks and resident memory per process name. It runs over every process every 4 s, so it reads
// /proc/<pid>/stat into one reused buffer and picks the three fields it needs without splitting the line.
func scanProcs() map[string]*procAgg {
	out := map[string]*procAgg{}
	d, err := os.Open("/proc")
	if err != nil {
		return out
	}
	pids, _ := d.Readdirnames(-1) // unsorted: os.ReadDir would sort ~1000 names for nothing
	d.Close()
	buf := make([]byte, 2048)
	for _, pid := range pids {
		if pid[0] < '0' || pid[0] > '9' {
			continue
		}
		name, ticks, rss, ok := parseStat(readSmall("/proc/"+pid+"/stat", buf))
		if !ok {
			continue
		}
		a := out[string(name)]
		if a == nil {
			a = &procAgg{}
			out[string(name)] = a
		}
		a.ticks += ticks
		a.rss += rss * 4096
	}
	return out
}

// readSmall reads a small /proc file into buf and returns its bytes (nil if it cannot be read).
func readSmall(path string, buf []byte) []byte {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil
	}
	defer syscall.Close(fd)
	n := 0
	for n < len(buf) {
		k, err := syscall.Read(fd, buf[n:])
		if k <= 0 || err != nil {
			break
		}
		n += k
	}
	return buf[:n]
}

// parseStat picks the command name, utime+stime and rss out of a /proc/<pid>/stat line. The name sits in
// parentheses and may itself hold spaces and parentheses, so the fields are counted from the last ')'.
func parseStat(s []byte) (name []byte, ticks, rss uint64, ok bool) {
	l, r := bytes.IndexByte(s, '('), bytes.LastIndexByte(s, ')')
	if l < 0 || r < l {
		return nil, 0, 0, false
	}
	var ut, st uint64
	field := 0 // fields after the name: 0 state ... 11 utime, 12 stime, 21 rss
	for i := r + 1; field <= 21; field++ {
		for i < len(s) && isSpace(s[i]) {
			i++
		}
		j := i
		for j < len(s) && !isSpace(s[j]) {
			j++
		}
		if j == i {
			return nil, 0, 0, false // fewer than 22 fields
		}
		switch field {
		case 11:
			ut = parseU(s[i:j])
		case 12:
			st = parseU(s[i:j])
		case 21:
			rss = parseU(s[i:j])
		}
		i = j
	}
	return s[l+1 : r], ut + st, rss, true
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\n' || c == '\t' || c == '\r' || c == '\v' || c == '\f'
}

// parseU parses a decimal number; anything else gives 0, as strconv.ParseUint's error case did.
func parseU(b []byte) uint64 {
	var v uint64
	for _, c := range b {
		if c < '0' || c > '9' {
			return 0
		}
		v = v*10 + uint64(c-'0')
	}
	return v
}

// dockerClient talks to the Docker engine over its unix socket.
var dockerClient = &http.Client{
	Timeout: time.Second,
	Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", "/var/run/docker.sock")
	}},
}

func queryDocker() dockerInfo {
	resp, err := dockerClient.Get("http://docker/containers/json?all=1")
	if err != nil {
		return dockerInfo{}
	}
	defer resp.Body.Close()
	var cs []struct {
		ID     string
		Names  []string
		Labels map[string]string
		State  string
		Status string
	}
	if json.NewDecoder(resp.Body).Decode(&cs) != nil {
		return dockerInfo{}
	}
	d := dockerInfo{ok: true, total: len(cs)}
	for _, c := range cs {
		name := c.Labels["com.docker.compose.service"]
		if name == "" && len(c.Names) > 0 {
			name = strings.TrimPrefix(c.Names[0], "/")
		}
		if len(name) > 9 {
			name = name[:9]
		}
		switch {
		case c.State == "restarting":
			d.restarting++
			d.badNames = append(d.badNames, name)
		case c.State == "exited" && !strings.Contains(c.Status, "Exited (0)"):
			d.bad++
			d.badNames = append(d.badNames, name)
		}
		if c.State != "running" {
			continue
		}
		d.up++
		if strings.Contains(c.Status, "(unhealthy)") {
			d.unhealthy++
			d.badNames = append(d.badNames, name)
		}
		if m, err := strconv.ParseFloat(strings.TrimSpace(readFile("/sys/fs/cgroup/system.slice/docker-"+c.ID+".scope/memory.current")), 64); err == nil {
			d.top = append(d.top, nameVal{name, m})
		}
	}
	sort.Slice(d.top, func(i, j int) bool { return d.top[i].v > d.top[j].v })
	d.top = d.top[:min(len(d.top), 4)]
	return d
}

// psi returns the "some" avg10 for cpu/memory/io and the "full" avg10 for memory/io.
func psi() (some [3]float64, fullMem, fullIO float64) {
	for i, n := range []string{"cpu", "memory", "io"} {
		for _, l := range strings.Split(readFile("/proc/pressure/"+n), "\n") {
			f := strings.Fields(l)
			if len(f) < 2 {
				continue
			}
			v, _ := strconv.ParseFloat(strings.TrimPrefix(f[1], "avg10="), 64)
			switch {
			case f[0] == "some":
				some[i] = v
			case f[0] == "full" && n == "memory":
				fullMem = v
			case f[0] == "full" && n == "io":
				fullIO = v
			}
		}
	}
	return
}

// nvmeTemp returns the hottest NVMe temperature in C.
func nvmeTemp() int {
	hot := 0
	for i := 0; i < 16; i++ {
		h := "/sys/class/hwmon/hwmon" + strconv.Itoa(i)
		if strings.TrimSpace(readFile(h+"/name")) == "nvme" {
			t, _ := strconv.Atoi(strings.TrimSpace(readFile(h + "/temp1_input")))
			hot = max(hot, t/1000)
		}
	}
	return hot
}
