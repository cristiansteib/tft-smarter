package main

import (
	"fmt"
	"time"
)

// netHist keeps one cumulative sample per minute for the last hour, so the network page can show
// "last hour" transfer and the busiest rate of that hour next to the since-boot totals.
type netHist struct {
	rx, tx [61]uint64 // cumulative bytes, ring of per-minute samples
	pd, pu [60]int    // per-minute peak rates, KB/s
	n      int        // samples stored (<= 61)
	head   int
	last   time.Time
	curD   int
	curU   int
}

// update is called every tick with the cumulative counters and the current rates (KB/s).
func (h *netHist) update(now time.Time, rx, tx uint64, down, up int) {
	h.curD, h.curU = max(h.curD, down), max(h.curU, up)
	if !h.last.IsZero() && now.Sub(h.last) < time.Minute {
		return
	}
	h.head = (h.head + 1) % 61
	h.rx[h.head], h.tx[h.head] = rx, tx
	h.pd[h.head%60], h.pu[h.head%60] = h.curD, h.curU
	h.curD, h.curU = 0, 0
	h.n = min(h.n+1, 61)
	h.last = now
}

// hour returns the transfer over the stored window (up to 60 min) and the peak rates in it.
func (h *netHist) hour(rx, tx uint64) (dRx, dTx uint64, peakD, peakU int) {
	if h.n > 0 {
		old := (h.head - (h.n - 1) + 61) % 61
		dRx, dTx = rx-min(rx, h.rx[old]), tx-min(tx, h.tx[old])
	}
	for i := 0; i < min(h.n, 60); i++ {
		peakD, peakU = max(peakD, h.pd[i]), max(peakU, h.pu[i])
	}
	return dRx, dTx, max(peakD, h.curD), max(peakU, h.curU)
}

// avgN is a 3 s moving average with a dead band: the shown value only moves when the average moved by more
// than ~8 %, so text readouts do not flicker (and are not retransmitted) for every wiggle.
type avgN struct {
	ring  [12]float64
	i     int
	shown float64
}

func (a *avgN) add(v float64) float64 {
	a.ring[a.i] = v
	a.i = (a.i + 1) % len(a.ring)
	sum := 0.0
	for _, x := range a.ring {
		sum += x
	}
	avg := sum / float64(len(a.ring))
	if d := avg - a.shown; d > a.shown/12+2 || -d > a.shown/12+2 {
		a.shown = avg
	}
	return a.shown
}

func fmtBytes(b uint64) string {
	switch {
	case b >= 1<<40:
		return fmt.Sprintf("%.2f TB", float64(b)/(1<<40))
	case b >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(b)/(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%.0f MB", float64(b)/(1<<20))
	}
	return fmt.Sprintf("%.0f KB", float64(b)/(1<<10))
}

func fmtKBs(kbs int) string {
	if kbs >= 1024 {
		return fmt.Sprintf("%.1f MB/s", float64(kbs)/1024)
	}
	return fmt.Sprintf("%d KB/s", kbs)
}
