package main

import (
	"encoding/json"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type nameVal struct {
	name string
	v    float64
}

type dockerInfo struct {
	ok                                             bool
	total, up, healthy, unhealthy, restarting, bad int
	badNames                                       []string
	top                                            []nameVal // by memory, bytes
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
	memTotal float64 // bytes
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

// scanProcs sums CPU ticks and resident memory per process name.
func scanProcs() map[string]*procAgg {
	out := map[string]*procAgg{}
	ents, _ := os.ReadDir("/proc")
	for _, e := range ents {
		if e.Name()[0] < '0' || e.Name()[0] > '9' {
			continue
		}
		s := readFile("/proc/" + e.Name() + "/stat")
		l, r := strings.IndexByte(s, '('), strings.LastIndexByte(s, ')')
		if l < 0 || r < l {
			continue
		}
		f := strings.Fields(s[r+1:])
		if len(f) < 22 {
			continue
		}
		ut, _ := strconv.ParseUint(f[11], 10, 64)
		st, _ := strconv.ParseUint(f[12], 10, 64)
		rss, _ := strconv.ParseUint(f[21], 10, 64)
		name := s[l+1 : r]
		a := out[name]
		if a == nil {
			a = &procAgg{}
			out[name] = a
		}
		a.ticks += ut + st
		a.rss += rss * 4096
	}
	return out
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
		} else if strings.Contains(c.Status, "(healthy)") {
			d.healthy++
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
