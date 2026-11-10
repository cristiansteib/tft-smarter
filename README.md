# tft-smarter

PC status dashboard for an Arduino Mega 2560 with a 3.5" ILI9486 TFT shield (480x320).

Parametric desktop enclosure: see [enclosure/README.md](enclosure/README.md)
for the Blender project, printable parts, dimensions, and fit-check procedure.
Parts and shopping links: [BOM.md](BOM.md).

- `src/main.cpp` – firmware (PlatformIO, MCUFRIEND_kbv, Adafruit TouchScreen): gauges, load scope, full-screen pages, touch gestures, and a split edge bar for Claude Code weekly (W) and 5-hour session (S) usage. Text is composed with its background in one pass (no blank-then-draw flicker) and repainted only where it changed.
- `cmd/tft-monitor` – Go program that runs on the PC and streams stats over USB serial (stats every 0.25 s, Claude/Docker data every ~2 s), lays out the screens and handles touch.
- `desktop-ui` – Tauri + Angular desktop app.

## Protocol

Binary frames, `0xAA 0x55 | type | len | payload | crc8` (CRC-8, poly 0x07) — see `cmd/tft-monitor/protocol.go`.
The Mega drops any frame with a bad CRC and reports `ok=N bad=M` back every 5 s (`TFT_VERBOSE=1 tft-monitor` prints it).

## Screens

The PC decides what the display shows; the Mega only draws what it is sent. Pages open **on demand** and go back home by themselves.

- **Dashboard** (home): CPU/RAM/GPU gauges with load averages, temperatures, swap, VRAM and power, load history scope, network rates, a two-line band (the four fullest disks, or an alert) and the menu strip.
- **CPU**: usage, temperature, load 1/5/15, load per core, top processes. **RAM**: RAM, swap, GPU memory, memory stalls, top processes. **Network**: transfer since boot and in the last hour, a live traffic scope (download/upload, autoscaled from 100 Kbps to 10 Gbps) and the current rates. Rates are bits per second in SI units, shown in the most readable unit with at most three significant digits (`287 Kbps`, `12.3 Mbps`, `941 Mbps`, `1.00 Gbps`) — the same convention as fast.com and speed tests; byte totals are decimal (`104.1 GB`). **Claude**: context window, 5-hour session with a live countdown to the reset, weekly usage and its reset, today's pace against budget, projected week, burn rate / chance of hitting the limit, open sessions, and the Claude logo. **System**, **Docker** and **Busiest** are there too.
- **Hogging CPU / RAM** come up by themselves when the machine is busy (load per core or RAM over the trigger and a process over its threshold), list only the real hogs, and go away once things calm down. The CPU one adds the load scope and RAM/swap bars. A page opened by hand stays at least 10 s.
- Scopes are real time (one sample per 250 ms frame); every text readout is steadier: network rates are a 3 s average that only moves when the traffic moves by more than ~8 %, and a bar that shifts by one point is not retransmitted.
- A page you open stays while you keep touching and returns home after *Return home after* (60 s) without a touch; pin a page to keep it up as long as you like (handy for watching the network).
- Alerts (disk full, temperatures, memory/swap, load, unhealthy containers, no internet…) take over the dashboard band with hysteresis. Push your own message:

```
tft-monitor notify -sev warn -ttl 60 "build finished"     # sev: info|warn|crit
```

## Touch

The panel is a 4-wire resistive touchscreen (shield pins XP=8, XM=A2, YP=A3, YM=9, via Adafruit TouchScreen). On the first boot it asks for three taps (top-left, top-right, bottom-left) and stores the calibration in EEPROM. Hold a finger anywhere for 6 s to calibrate again, or use *Setup → Calibrate touch*.

**Bottom menu**: a thin strip on every screen — Home, CPU, RAM, Net, Claude, System, Setup. Tap an entry to open it. **Tap the highlighted entry (the page you are on) to pin that page**; a small pin appears on the tab (and `pin` in the title). Tap it again to release. Any screen can be pinned, including Home and Setup. Navigating somewhere else lets go of the pin, and double-tapping a gauge on the dashboard opens its page already pinned.

| On the dashboard, tap… | Opens |
| --- | --- |
| the CPU gauge | CPU |
| the RAM gauge | RAM |
| the GPU gauge | System |
| the network rates | Network |
| "claude" in the header, the left edge bar, or the usage line under the network | Claude |
| "docker" in the header | Docker |
| the load scope | Busiest |

| On any other page | Does |
| --- | --- |
| tap the title | back home |
| swipe left / right | next / previous page |
| double-tap the body, or hold ~1 s | **pin** the page: it stays up (no return home, no alert takeover); do it again to let go |

**Setup** is navigable by touch: swipe or tap *Next page* for its three sub-pages — *General* (return-home time, hog-page alternation, **Calibrate touch**, redraw), *Thresholds* (CPU hog %, load and RAM triggers, RAM hog %) and *Pages* (allow or hide individual pages). Tap a row to toggle it or run it; on `<  value  >` rows tap the left half of the value to decrease and the right half to increase. Tap the title to leave. Everything is saved immediately.

## Desktop app and control socket

`tft-monitor` listens on `$XDG_RUNTIME_DIR/tft-monitor.ctl` (newline-delimited JSON: `state`, `settings`, `set`, `pin`, `next`, `prev`, `notify`). Settings (hog thresholds, load/RAM triggers, page time, disabled pages) are saved in `~/.config/tft-monitor/settings.json`.

`desktop-ui/` is a Tauri 2 + Angular app on top of it: live mirror of the TFT, notifications, page control and thresholds, service start/stop and logs.

```
mise run ui           # dev mode
mise run ui-build     # release binary in desktop-ui/src-tauri/target/release
```

## Install the host monitor with mise

Install the host monitor from the public repository:

```
mise use -g go:github.com/cristiansteib/tft-smarter/cmd/tft-monitor@latest
tft-monitor -port /dev/ttyACM0      # TFT_VERBOSE=1 prints what it sends
```

## Run at login/boot (systemd user service)

```
tft-monitor install -port /dev/ttyACM0     # writes ~/.config/systemd/user/tft-monitor.service, enables + starts it
tft-monitor uninstall
```

Re-run `install` after upgrading to a new version (the unit points at the installed binary). It also runs `loginctl enable-linger` so it starts at boot.

## Develop

```
mise run flash      # stop the service, upload firmware, start the service again
mise run monitor    # build + run the monitor
```

## Screensaver

When the PC powers off, reboots or the service stops, `tft-monitor` gets SIGTERM and sends the board a summary
(frame 6: uptime, estimated Wh since boot, CPU/GPU peaks, CPU average, network totals, fullest disk) and a "bye"
(frame 7: reason + clock). The board then runs a screensaver that rotates five pages: a starfield with an orbiting
comet and the time since power-off, the last session summary (count-up numbers), animated peak bars, the energy
as phone-charge batteries, and a sleeping moon with floating Zs. The same screensaver starts if the link is simply
lost for 5 s ("LINK LOST") and ends as soon as the monitor sends data again.
The summary is refreshed every 30 s, so it also works if the USB power drops without a clean goodbye.
