// tft-monitor streams PC stats to the Arduino TFT dashboard over USB serial using small
// CRC-checked binary frames (see protocol.go): stats every 0.25 s, Claude/Docker every ~2 s,
// disks every ~2 s (each resent at least every 5 s).
package main

import (
	"bufio"
	"bytes"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"go.bug.st/serial"
)

func readFile(p string) string { b, _ := os.ReadFile(p); return string(b) }

func cpuTimes() (idle, total uint64) {
	line, _, _ := strings.Cut(readFile("/proc/stat"), "\n")
	for i, f := range strings.Fields(line)[1:] {
		v, _ := strconv.ParseUint(f, 10, 64)
		total += v
		if i == 3 || i == 4 {
			idle += v
		}
	}
	return
}

// memPct returns RAM and swap usage in percent.
func memPct() (ram, swap int) {
	var total, avail, swTotal, swFree float64
	for _, l := range strings.Split(readFile("/proc/meminfo"), "\n") {
		f := strings.Fields(l)
		if len(f) < 2 {
			continue
		}
		v, _ := strconv.ParseFloat(f[1], 64)
		switch f[0] {
		case "MemTotal:":
			total = v
		case "MemAvailable:":
			avail = v
		case "SwapTotal:":
			swTotal = v
		case "SwapFree:":
			swFree = v
		}
	}
	if total > 0 {
		ram = int(100*(total-avail)/total + 0.5)
	}
	if swTotal > 0 {
		swap = int(100*(swTotal-swFree)/swTotal + 0.5)
	}
	return
}

// loadAvg returns the 1/5/15 minute load averages times 100.
func loadAvg() (l [3]int) {
	f := strings.Fields(readFile("/proc/loadavg"))
	for i := 0; i < 3 && i < len(f); i++ {
		v, _ := strconv.ParseFloat(f[i], 64)
		l[i] = int(v*100 + 0.5)
	}
	return
}

func netBytes() (rx, tx uint64) {
	lines := strings.Split(readFile("/proc/net/dev"), "\n")
	for _, l := range lines[min(2, len(lines)):] {
		name, data, ok := strings.Cut(l, ":")
		name = strings.TrimSpace(name)
		if !ok || name == "lo" || strings.HasPrefix(name, "br-") || strings.HasPrefix(name, "veth") ||
			strings.HasPrefix(name, "docker") || strings.HasPrefix(name, "zcc") {
			continue
		}
		f := strings.Fields(data)
		if len(f) < 9 {
			continue
		}
		r, _ := strconv.ParseUint(f[0], 10, 64)
		t, _ := strconv.ParseUint(f[8], 10, 64)
		rx += r
		tx += t
	}
	return
}

func cpuTempFile() string {
	hs, _ := filepath.Glob("/sys/class/hwmon/hwmon*")
	for _, h := range hs {
		if strings.TrimSpace(readFile(h+"/name")) == "coretemp" {
			return h + "/temp1_input"
		}
	}
	return ""
}

type disk struct {
	name string
	pct  int
}

// disks returns the real disk filesystems (like df's Use%), fullest first, at most 8.
func disks() []disk {
	real := map[string]bool{"ext2": true, "ext3": true, "ext4": true, "xfs": true, "btrfs": true, "f2fs": true,
		"ntfs": true, "ntfs3": true, "fuseblk": true, "exfat": true, "zfs": true}
	seen := map[string]bool{}
	var out []disk
	for _, l := range strings.Split(readFile("/proc/mounts"), "\n") {
		f := strings.Fields(l)
		if len(f) < 3 || !real[f[2]] || seen[f[0]] || f[1] == "/boot" || strings.HasPrefix(f[1], "/boot/") {
			continue
		}
		seen[f[0]] = true
		var s syscall.Statfs_t
		if syscall.Statfs(f[1], &s) != nil || s.Blocks == 0 {
			continue
		}
		used := s.Blocks - s.Bfree
		if used+s.Bavail == 0 {
			continue
		}
		name := "root"
		if f[1] != "/" {
			name = filepath.Base(f[1])
			if parts := strings.Split(strings.Trim(f[1], "/"), "/"); len(parts) == 2 && parts[0] != "mnt" && parts[0] != "media" {
				name = parts[0] + "/" + parts[1] // e.g. var/lib
			}
		}
		if len(name) > 9 {
			name = name[:9]
		}
		out = append(out, disk{strings.ReplaceAll(name, ",", "_"), int((100*used + used + s.Bavail - 1) / (used + s.Bavail))})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].pct > out[j].pct })
	return out[:min(len(out), 8)]
}

func gpuSm(e *ema, v int, alpha float64) int { return e.add(float64(v), alpha) }

// ema is an exponential moving average; alpha closer to 0 = smoother.
type ema struct {
	v    float64
	init bool
}

func (e *ema) add(x, alpha float64) int {
	if !e.init {
		e.v, e.init = x, true
	} else {
		e.v += alpha * (x - e.v)
	}
	return int(e.v + 0.5)
}

type gpuInfo struct{ util, temp, vram, watts, limit int }

func queryGPU() gpuInfo {
	out, err := exec.Command("nvidia-smi", "--query-gpu=utilization.gpu,temperature.gpu,memory.used,memory.total,power.draw,enforced.power.limit",
		"--format=csv,noheader,nounits").Output()
	if err != nil {
		return gpuInfo{}
	}
	f := strings.Split(strings.TrimSpace(string(out)), ",")
	if len(f) < 6 {
		return gpuInfo{}
	}
	n := make([]float64, 6)
	for i := range n {
		n[i], _ = strconv.ParseFloat(strings.TrimSpace(f[i]), 64)
	}
	if n[3] == 0 {
		return gpuInfo{}
	}
	return gpuInfo{int(n[0]), int(n[1]), int(100*n[2]/n[3] + 0.5), int(n[4] + 0.5), int(n[5] + 0.5)}
}

func memTotalBytes() float64 {
	for _, l := range strings.Split(readFile("/proc/meminfo"), "\n") {
		if f := strings.Fields(l); len(f) >= 2 && f[0] == "MemTotal:" {
			v, _ := strconv.ParseFloat(f[1], 64)
			return v * 1024
		}
	}
	return 1
}

var wmu sync.Mutex
var byeFn atomic.Value // func(), set while a serial port is open

var (
	collectors *bg
	eng        *engine
	pan        *panel
)

func run(port serial.Port, cpuT string) error {
	idle0, tot0 := cpuTimes()
	rx0, tx0 := netBytes()
	t0 := time.Now()
	gpu := queryGPU()
	var eCPU, eCPUT, eGPU, eGPUT, eGPUW ema
	var lastC []byte
	var lastCT time.Time
	snap := &snapshot{memTotal: memTotalBytes(), ncpu: runtime.NumCPU()}
	en := loadEnergy()
	var uptime uint64
	mkSum := func() []byte {
		rx, tx := netBytes()
		d := 0
		if len(snap.disks) > 0 {
			d = snap.disks[0].pct
		}
		return summaryFrame(uint32(uptime), uint32(en.Wh*10+0.5), [5]int{en.PeakCPU, en.PeakGPU, en.PeakCT, en.PeakGT, en.avgCPU()}, rx>>20, tx>>20, d)
	}
	pan.name, pan.shown, pan.pending = "", map[byte]cell{}, nil
	send := func(b []byte) error {
		wmu.Lock()
		defer wmu.Unlock()
		_, err := port.Write(b)
		if os.Getenv("TFT_VERBOSE") != "" {
			fmt.Printf("% x\n", b)
		}
		return err
	}
	// on SIGTERM (power off, reboot, service stop) tell the board, so it can start its screensaver
	byeFn.Store(func() {
		t := time.Now()
		reason := shutdownReason()
		en.save()
		for i := 0; i < 2; i++ {
			send(mkSum())
			send(byeFrame(reason, t.Hour(), t.Minute()))
		}
		wmu.Lock()
		port.Drain()
		wmu.Unlock()
	})
	// the board reports "ok=N bad=M" (frames accepted / rejected by CRC) every 5 s
	go func() {
		r := bufio.NewReader(port)
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			if os.Getenv("TFT_VERBOSE") != "" {
				fmt.Print("board: ", line)
			}
		}
	}()

	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	for n := 1; ; n++ {
		<-tick.C
		now := time.Now()
		dt := now.Sub(t0).Seconds()
		t0 = now
		if n%8 == 1 {
			snap.cl = claudeStats()
			snap.disks = disks()
			snap.psi, snap.fullMem, snap.fullIO = psi()
			snap.nvmeT = nvmeTemp()
			docker := -1
			if snap.dock.ok {
				docker = snap.dock.up
			}
			if c := claudeFrame(snap.cl, docker); !bytes.Equal(c, lastC) || now.Sub(lastCT) > 5*time.Second {
				if err := send(c); err != nil {
					return err
				}
				lastC, lastCT = c, now
			}
		}
		if n%4 == 1 { // nvidia-smi once a second is plenty
			gpu = queryGPU()
			gpu.util, gpu.temp = gpuSm(&eGPU, gpu.util, 0.5), gpuSm(&eGPUT, gpu.temp, 0.4)
			gpu.watts = gpuSm(&eGPUW, gpu.watts, 0.5)
			snap.gpuW, snap.gpuLimit = gpu.watts, gpu.limit
		}
		idle, tot := cpuTimes()
		cpu := 0
		if tot > tot0 {
			cpu = int(100*(1-float64(idle-idle0)/float64(tot-tot0)) + 0.5)
		}
		idle0, tot0 = idle, tot
		en.add(now, dt, cpu, gpu.watts)
		snap.pcW, snap.whBoot = int(en.pcW+0.5), en.Wh
		cpu = eCPU.add(float64(cpu), 0.5)
		rx, tx := netBytes()
		down, up := int(float64(rx-rx0)/1024/dt), int(float64(tx-tx0)/1024/dt)
		rx0, tx0 = rx, tx
		temp := 0
		if cpuT != "" {
			t, _ := strconv.Atoi(strings.TrimSpace(readFile(cpuT)))
			temp = eCPUT.add(float64(t)/1000, 0.15)
		}
		upS, _, _ := strings.Cut(readFile("/proc/uptime"), ".")
		uptime, _ = strconv.ParseUint(upS, 10, 32)
		ram, swap := memPct()
		load := loadAvg()
		snap.cpu, snap.ram, snap.swap, snap.cpuT = cpu, ram, swap, temp
		snap.gpu, snap.gpuT, snap.vram, snap.load = gpu.util, gpu.temp, gpu.vram, load
		en.sample(cpu, gpu.util, temp, gpu.temp)
		snap.dock, snap.top, snap.latency, snap.latFails = collectors.get()
		if err := send(statsFrame(cpu, ram, swap, temp, gpu.util, gpu.temp, gpu.vram, down, up,
			uint32(uptime), now.Hour(), now.Minute(), load, snap.ncpu, gpu.watts, gpu.limit)); err != nil {
			return err
		}
		if n%4 == 0 {
			eng.eval(snap, now)
		}
		if n%120 == 8 { // the screensaver summary, refreshed every 30 s
			if err := send(mkSum()); err != nil {
				return err
			}
		}
		for _, f := range pan.tick(now, snap, eng) {
			if err := send(f); err != nil {
				return err
			}
		}
		latest.Store(pan.doc(snap, eng, down, up, uint32(uptime), now))
	}
}

func main() {
	portName := flag.String("port", "/dev/ttyACM0", "serial port of the Arduino")
	sev := flag.String("sev", "info", "notify: severity info|warn|crit")
	ttl := flag.Int("ttl", 30, "notify: seconds to keep the notification")
	cmd := ""
	if len(os.Args) > 1 && !strings.HasPrefix(os.Args[1], "-") {
		cmd = os.Args[1]
		os.Args = append(os.Args[:1:1], os.Args[2:]...)
	}
	flag.Parse()
	switch cmd {
	case "install":
		if err := installService(*portName); err != nil {
			log.Fatal(err)
		}
		return
	case "uninstall":
		if err := uninstallService(); err != nil {
			log.Fatal(err)
		}
		return
	case "notify":
		if err := sendNotify(*sev, *ttl, strings.Join(flag.Args(), " ")); err != nil {
			log.Fatal(err)
		}
		return
	case "":
	default:
		log.Fatalf("unknown command %q (use: install, uninstall, notify)", cmd)
	}
	cpuT := cpuTempFile()
	collectors, eng, pan = startCollectors(), newEngine(), newPanel()
	loadCfg()
	listenNotify(eng)
	listenCtl(eng)
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		<-sigCh
		if f, ok := byeFn.Load().(func()); ok {
			f()
		}
		os.Exit(0)
	}()
	bw := bufio.NewWriter(os.Stderr)
	defer bw.Flush()
	for {
		p, err := serial.Open(*portName, &serial.Mode{BaudRate: 115200})
		if err == nil {
			time.Sleep(2500 * time.Millisecond) // the board resets when the port opens
			err = run(p, cpuT)
			p.Close()
		}
		disconnected()
		log.Printf("serial error: %v - retrying", err)
		time.Sleep(2 * time.Second)
	}
}
