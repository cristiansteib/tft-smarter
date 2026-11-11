// Package version says which build this is and which other parts it works with.
//
// Three things have to fit together: the firmware on the board, the daemon (tft-monitor) that drives it over
// serial, and the clients (tft-notify, the desktop app) that talk to the daemon over its control socket.
//
//   - Proto is the serial protocol: the firmware reports its own (a "V <proto> <version>" line every 5 s) and the
//     daemon drives any firmware from MinProto to Proto. Bump Proto when the firmware gains or changes a frame;
//     raise MinProto only when the daemon can no longer drive the older firmware.
//   - API is the control socket: bump it when a command is added or changed.
package version

import (
	"runtime/debug"
	"strconv"
	"strings"
)

const (
	Proto    = 2 // 2: frameGeo, the notice layout
	MinProto = 2
	API      = 1
)

// Version is set by the release build (-ldflags -X); empty in a build from source.
var Version = ""

// Get is this build's version: the release's ("0.16.0"), the module version for `go install …@v0.16.0`, or "dev".
func Get() string {
	if Version != "" {
		return strings.TrimPrefix(Version, "v")
	}
	if bi, ok := debug.ReadBuildInfo(); ok && Released(bi.Main.Version) {
		return strings.TrimPrefix(bi.Main.Version, "v")
	}
	return "dev"
}

// Released is true for a version that came from a release. A build from source is neither older nor newer than
// one, and is never updated on its own.
func Released(v string) bool {
	_, ok := parse(v)
	return ok && !strings.ContainsAny(v, "-+") // pre-releases, snapshots and go's pseudo-versions are not releases
}

// Newer reports whether a is a later release than b. A b that does not parse ("dev") is older than everything.
func Newer(a, b string) bool {
	x, ok := parse(a)
	if !ok {
		return false
	}
	y, ok := parse(b)
	if !ok {
		return true
	}
	for i := range x {
		if x[i] != y[i] {
			return x[i] > y[i]
		}
	}
	return false
}

// parse reads major.minor.patch, with or without a leading "v", ignoring any pre-release or build suffix.
func parse(v string) ([3]int, bool) {
	var out [3]int
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

// Fits says whether a daemon that drives firmware protocols min..max can drive a board that speaks proto, and if
// not, what to do about it.
func Fits(proto, min, max int) (bool, string) {
	switch {
	case proto < min:
		return false, "the board's firmware is too old for this tft-monitor: flash it (`tft-monitor flash`)"
	case proto > max:
		return false, "tft-monitor is too old for the board's firmware: update it (`tft-notify self-update`)"
	}
	return true, ""
}
