package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// energy integrates an *estimate* of the whole PC's power draw. The GPU is measured (nvidia-smi);
// the CPU is modelled from utilisation and the rest of the machine (board, RAM, SSDs, fans)
// is a constant. These generic starting values need calibration for the target system.
// Good for trends, not an invoice.
const (
	cpuIdleW = 15.0  // estimated CPU package watts at idle
	cpuFullW = 125.0 // estimated CPU package watts at full load
	restW    = 35.0  // board, RAM, drives, fans
)

type energy struct {
	BootID string  `json:"boot_id"`
	Wh     float64 `json:"wh"` // since this boot
	// session stats for the screensaver summary, also per boot
	PeakCPU, PeakGPU, PeakCT, PeakGT int
	CPUSum                           float64
	N                                int64
	saved                            time.Time
	path                             string
}

func loadEnergy() *energy {
	e := &energy{}
	if d, err := os.UserHomeDir(); err == nil {
		e.path = filepath.Join(d, ".local", "state", "tft-monitor", "energy.json")
		if b, err := os.ReadFile(e.path); err == nil {
			json.Unmarshal(b, e)
		}
	}
	// the counter is per boot: a new boot id starts again from zero, a service restart carries on
	if id := strings.TrimSpace(readFile("/proc/sys/kernel/random/boot_id")); id != e.BootID {
		*e = energy{path: e.path, BootID: id}
	}
	return e
}

// add accounts dt seconds at the given raw CPU utilisation (0-100) and GPU watts.
func (e *energy) add(now time.Time, dt float64, cpuPct, gpuW int) {
	w := cpuIdleW + (cpuFullW-cpuIdleW)*float64(cpuPct)/100 + float64(gpuW) + restW
	e.Wh += w * dt / 3600
	if now.Sub(e.saved) >= time.Minute {
		e.saved = now
		e.save()
	}
}

func (e *energy) save() {
	if e.path == "" {
		return
	}
	os.MkdirAll(filepath.Dir(e.path), 0o755)
	b, _ := json.Marshal(e)
	os.WriteFile(e.path, b, 0o644)
}

// sample records the peaks and the running CPU average for the shutdown summary.
func (e *energy) sample(cpu, gpu, cpuT, gpuT int) {
	e.PeakCPU, e.PeakGPU, e.PeakCT, e.PeakGT = max(e.PeakCPU, cpu), max(e.PeakGPU, gpu), max(e.PeakCT, cpuT), max(e.PeakGT, gpuT)
	e.CPUSum += float64(cpu)
	e.N++
}

func (e *energy) avgCPU() int {
	if e.N == 0 {
		return 0
	}
	return int(e.CPUSum/float64(e.N) + 0.5)
}

// shutdownReason tells a real power-off/reboot from someone just stopping the service.
func shutdownReason() byte {
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	out, _ := exec.CommandContext(ctx, "systemctl", "list-jobs", "--no-legend").Output()
	s := string(out)
	switch {
	case strings.Contains(s, "reboot.target"):
		return 2
	case strings.Contains(s, "poweroff.target"), strings.Contains(s, "halt.target"), strings.Contains(s, "shutdown.target"):
		return 1
	}
	return 3
}
