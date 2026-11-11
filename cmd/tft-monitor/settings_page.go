package main

import (
	"fmt"
	"maps"
	"strings"
	"time"
)

// The settings page is drawn by the daemon like every other full-screen page; taps on its rows change the
// settings (saved at once). Three sub-pages: general, thresholds, and which pages are allowed to show.
const (
	settingsSubs = 3
	rowH         = 31 // must match LROW in the firmware
)

type sitem struct {
	label string
	kind  byte // 't' toggle, 's' stepper (tap the left / right part of the value), 'a' action
	key   string
}

var settingsItems = [settingsSubs][]sitem{
	{
		{"Return home after", 's', "return"},
		{"Hog pages alternate", 's', "dwell"},
		{"Calibrate touch", 'a', "cal"},
		{"Redraw screen", 'a', "redraw"},
	},
	{
		{"CPU hog above", 's', "hogCpu"},
		{"Hog pages when load", 's', "load"},
		{"RAM hog above", 's', "hogMem"},
		{"RAM pages when used", 's', "ram"},
	},
	{
		{"CPU page", 't', "pg:cpu"},
		{"RAM page", 't', "pg:ram"},
		{"Network page", 't', "pg:net"},
		{"Claude page", 't', "pg:claude"},
		{"CPU hogs (when busy)", 't', "pg:rollcpu"},
		{"RAM hogs (when busy)", 't', "pg:rollmem"},
	},
}

var subTitle = [settingsSubs]string{"General", "Thresholds", "Pages"}

func onOff(v bool) (string, byte) {
	if v {
		return "On", pGreen
	}
	return "Off", pGrey
}

func stepper(v string) string { return "<  " + v + "  >" }

// settingValue is what a row shows on the right.
func settingValue(it sitem, c settings) (string, byte) {
	switch it.key {
	case "return":
		return stepper(fmt.Sprintf("%ds", c.ReturnSec)), pCyan
	case "dwell":
		return stepper(fmt.Sprintf("%ds", c.DwellSec)), pCyan
	case "cal":
		return "Start", pSky
	case "redraw":
		return "Go", pSky
	case "hogCpu":
		return stepper(fmt.Sprintf("%.0f%%", c.HogCPU)), pCyan
	case "load":
		return stepper(fmt.Sprintf("%.0f%%", c.LoadTrigger)), pCyan
	case "hogMem":
		return stepper(fmt.Sprintf("%.0f%%", c.HogMem)), pCyan
	case "ram":
		return stepper(fmt.Sprintf("%d%%", c.RamTrigger)), pCyan
	}
	if pg, ok := strings.CutPrefix(it.key, "pg:"); ok {
		return onOff(!c.Disabled[pg])
	}
	return "", pGrey
}

func (p *panel) buildSettings(sub int) []cell {
	sub = ((sub % settingsSubs) + settingsSubs) % settingsSubs
	c := cfg()
	cs := []cell{{0, pCyan | sBig, 0, fmt.Sprintf("Settings\t%s %d/%d", subTitle[sub], sub+1, settingsSubs)}}
	for i, it := range settingsItems[sub] {
		v, col := settingValue(it, c)
		cs = append(cs, cell{byte(i + 1), col, 0, it.label + "\t" + v})
	}
	cs = append(cs, cell{8, pGrey, 0, fmt.Sprintf("Next page\t%d/%d  >", sub%settingsSubs+1, settingsSubs)})
	return cs
}

// touchSettings handles a tap inside the settings page.
func (p *panel) touchSettings(x, y int, now time.Time) {
	row := (y - 4) / rowH
	switch {
	case row <= 0:
		p.leaveSettings(now)
		return
	case row >= 7: // footer
		p.sub = (p.sub + 1) % settingsSubs
		return
	}
	items := settingsItems[p.sub%settingsSubs]
	if row > len(items) {
		return
	}
	it := items[row-1]
	if it.kind == 's' && x < 300 { // toggles and actions: the whole row is the button; steppers: only the value
		return
	}
	c := cfg()
	sign := -1 // steppers: the left half of the value decreases, the right half increases
	if x >= 390 {
		sign = 1
	}
	switch it.key {
	case "return":
		c.ReturnSec += 15 * sign
	case "dwell":
		c.DwellSec += sign
	case "hogCpu":
		c.HogCPU += 5 * float64(sign)
	case "load":
		c.LoadTrigger += 5 * float64(sign)
	case "hogMem":
		c.HogMem += float64(sign)
	case "ram":
		c.RamTrigger += 5 * sign
	case "cal":
		p.pending = append(p.pending, cmdFrame(cmdCalibrate))
		return
	case "redraw":
		p.repaint = true
		return
	default:
		if pg, ok := strings.CutPrefix(it.key, "pg:"); ok {
			c.Disabled = maps.Clone(c.Disabled) // cfg()'s map is shared: change a copy
			c.Disabled[pg] = !c.Disabled[pg]
		}
	}
	setCfg(c)
}

func (p *panel) leaveSettings(now time.Time) {
	p.manual = ""
	if c := cfg(); c.Pin == "settings" { // the pinned page is the settings themselves: leaving lets go of it
		c.Pin = ""
		setCfg(c)
	}
	if cfg().Pin == "" {
		p.goTo("dash", now) // a pinned page is still there underneath: just uncover it
	}
}
