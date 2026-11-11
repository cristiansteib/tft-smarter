package main

import (
	"fmt"
	"time"
)

// netHist keeps one cumulative sample per minute for the last hour, so the network page can show
// "last hour" transfer next to the since-boot totals.
type netHist struct {
	rx, tx [61]uint64 // cumulative bytes, ring of per-minute samples
	n      int        // samples stored (<= 61)
	head   int
	last   time.Time
}

// update is called every tick with the cumulative counters.
func (h *netHist) update(now time.Time, rx, tx uint64) {
	if !h.last.IsZero() && now.Sub(h.last) < time.Minute {
		return
	}
	h.head = (h.head + 1) % 61
	h.rx[h.head], h.tx[h.head] = rx, tx
	h.n = min(h.n+1, 61)
	h.last = now
}

// hour returns the transfer over the stored window (up to 60 min).
func (h *netHist) hour(rx, tx uint64) (dRx, dTx uint64) {
	if h.n > 0 {
		old := (h.head - (h.n - 1) + 61) % 61
		dRx, dTx = rx-min(rx, h.rx[old]), tx-min(tx, h.tx[old])
	}
	return dRx, dTx
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

// fmtBytes formats a byte count in decimal units (1 GB = 1,000,000,000 bytes, the way ISPs and disk makers do) in the
// most readable unit with at most three significant digits: 999 B, 1.50 KB, 45.6 MB, 112 GB, 1.23 TB.
func fmtBytes(b uint64) string {
	v, i := float64(b), 0
	for v >= 999.5 && i < 4 {
		v /= 1000
		i++
	}
	unit := [...]string{"B", "KB", "MB", "GB", "TB"}[i]
	switch {
	case i == 0:
		return fmt.Sprintf("%d B", b)
	case v < 9.995:
		return fmt.Sprintf("%.2f %s", v, unit)
	case v < 99.95:
		return fmt.Sprintf("%.1f %s", v, unit)
	}
	return fmt.Sprintf("%.0f %s", v, unit)
}

// fmtBits formats a rate given in kbit/s the way network tools do (fast.com, speed tests): Kbps, Mbps, Gbps, SI units.
// The board's fmtRate() in main.cpp must keep the same thresholds and rounding.
func fmtBits(kbps int) string {
	switch {
	case kbps < 1000:
		return fmt.Sprintf("%d Kbps", kbps)
	case kbps < 99950:
		t := (kbps + 50) / 100
		return fmt.Sprintf("%d.%d Mbps", t/10, t%10)
	case kbps < 999500:
		return fmt.Sprintf("%d Mbps", (kbps+500)/1000)
	}
	g := (kbps + 5000) / 10000
	return fmt.Sprintf("%d.%02d Gbps", g/100, g%100)
}
