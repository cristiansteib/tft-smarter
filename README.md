# tft-smarter

PC status dashboard for an Arduino Mega 2560 with a 3.5" ILI9486 TFT shield (480x320).

Parametric desktop enclosure: see [enclosure/README.md](enclosure/README.md)
for the Blender project, printable parts, dimensions, and fit-check procedure.
Parts and shopping links: [BOM.md](BOM.md).

- `src/main.cpp` – firmware (PlatformIO, MCUFRIEND_kbv, Adafruit TouchScreen): gauges, load scope, full-screen pages, touch gestures, and a split edge bar for Claude Code weekly (W) and 5-hour session (S) usage. Text is composed with its background in one pass (no blank-then-draw flicker) and repainted only where it changed.
- `cmd/tft-monitor` – Go program that runs on the PC and streams stats over USB serial (stats every 0.25 s, Claude/Docker data every ~2 s), lays out the screens and handles touch.
- `cmd/tft-notify` – CLI to put notices on the screen (for people, scripts and coding agents); `internal/notify` is the message model both share.
- `desktop-ui` – Tauri + Angular desktop app.

## Protocol

Binary frames, `0xAA 0x55 | type | len | payload | crc8` (CRC-8, poly 0x07) — see `cmd/tft-monitor/protocol.go`.
The Mega drops any frame with a bad CRC and reports `ok=N bad=M` back every 5 s (`TFT_VERBOSE=1 tft-monitor` prints it), together with `V <protocol> <firmware version>`.

## Screens

The PC decides what the display shows; the Mega only draws what it is sent. Pages open **on demand** and go back home by themselves.

- **Dashboard** (home): CPU/RAM/GPU gauges with load averages, temperatures, swap, VRAM and power, load history scope, network rates, a two-line band (the four fullest disks, or an alert) and the menu strip.
- **CPU**: usage, temperature, load 1/5/15, load per core, top processes. **RAM**: RAM, swap, GPU memory, memory stalls, top processes. **Network**: transfer since boot and in the last hour, a live traffic scope (download/upload, autoscaled from 100 Kbps to 10 Gbps) and the current rates. Rates are bits per second in SI units, shown in the most readable unit with at most three significant digits (`287 Kbps`, `12.3 Mbps`, `941 Mbps`, `1.00 Gbps`) — the same convention as fast.com and speed tests; byte totals are decimal (`104.1 GB`). **Claude**: context window, 5-hour session with a live countdown to the reset, weekly usage with what is left and when it resets, the weekly outlook (how much you can burn per day against what you actually use, where your current pace ends up at the reset, and whether you would hit the limit before it), open sessions, and the Claude logo. **System**, **Docker** and **Busiest** are there too.
- **Hogging CPU / RAM** come up by themselves when the machine is busy (load per core or RAM over the trigger and a process over its threshold), list only the real hogs, and go away once things calm down. The CPU one adds the load scope and RAM/swap bars. A page opened by hand stays at least 10 s.
- Scopes are real time (one sample per 250 ms frame); every text readout is steadier: network rates are a 3 s average that only moves when the traffic moves by more than ~8 %, and a bar that shifts by one point is not retransmitted.
- A page you open stays while you keep touching and returns home after *Return home after* (60 s) without a touch; pin a page to keep it up as long as you like (handy for watching the network).
- Alerts (disk full, temperatures, memory/swap, load, unhealthy containers, no internet…) take over the dashboard band with hysteresis.
- **Notices** you (or an agent) send with `tft-notify` take over the whole screen; see below.

## Notifications: `tft-notify`

`cmd/tft-notify` is a small CLI that puts a notice on the screen through the monitor's control socket.

```
tft-notify "Coffee is ready"                                       # info, 20 s
tft-notify -l success -s ci "Deploy done" "api v2.3.1 is live"
tft-notify -l action -a "Approve in the terminal" "Push to main?"   # blinks, stays until tapped
tft-notify --id build -p 40 "Building firmware"                    # same id: updated in place
tft-notify -l action --wait --timeout 10m "Plug in the board"      # blocks until you tap it
make 2>&1 | tail -3 | tft-notify -l error                          # stdin becomes the text
tft-notify --json -m '{"level":"warn","title":"Disk 91%","fields":{"host":"nas"}}'
tft-notify list | get ID | wait ID | dismiss ID|--all | levels
```

| Level | Use | Colour | Stays |
| --- | --- | --- | --- |
| `info` | FYI | blue | 20 s |
| `success` | finished fine | teal | 30 s |
| `warn` | worth a look soon | amber | 2 min |
| `error` | something failed | coral | 10 min |
| `action` | you must act now; the header blinks | pink | until tapped |

Two layouts: **simple** (a big title and a short line) and **detail** (title, body, the action to take, a progress bar and up to six `label value` rows, with the source in the header); `--view auto` (the default) picks detail when there is an action, progress, fields or a long body. `-c` overrides the accent with `#RRGGBB` or a name, `-t` the time on screen (`0` = until tapped). The header shows the level, the source, the time and `2/3` when several are waiting, most urgent first.

On the screen: **tap** dismisses the notice, **swipe** shows the next one, **hold** dismisses them all. The menu still works: a page you open wins until a newer notice arrives, and once the notices are gone the screen returns to what was up before.

For agents: every command takes `--json` (one JSON object on stdout, errors included), `tft-notify --help-json` describes every command, flag, level, exit code and the message schema, and exit code 3 means the monitor is not running. `tft-notify agents-install` writes a short skill to `~/.claude/skills/tft-notify/` (and `~/.agents/skills/` when `~/.agents` exists; `--dir` for others, `--print` to see it, `--remove` to undo) that tells coding agents the screen is there and when to use it.

Every user of the machine can send notices: the monitor also listens on the abstract socket `@tft-monitor`, which takes only `notify`, `notices`, `notice`, `dismiss` and `version` (run it with `-shared=false` to keep it to your own user).

## Desktop app and control socket

`tft-monitor` listens on `$XDG_RUNTIME_DIR/tft-monitor.ctl` (newline-delimited JSON: `state`, `settings`, `set`, `pin`, `next`, `prev`, `goto`, `notify`, `notices`, `notice`, `dismiss`, `version`, `update`). Settings (hog thresholds, load/RAM triggers, page time, disabled pages) are saved in `~/.config/tft-monitor/settings.json`.

`desktop-ui/` is a Tauri 2 + Angular app on top of it: live mirror of the TFT, notifications, page control and thresholds, service start/stop and logs.

```
mise run ui           # dev mode
mise run ui-build     # release binary in desktop-ui/src-tauri/target/release
```

## Install and update

Releases are cut by [semantic-release](https://semantic-release.gitbook.io) from the commit messages on every push to `main` (`fix:` makes a patch release, `feat:` a minor one, `feat!:` or `BREAKING CHANGE:` a major one; other commits release nothing). GoReleaser then attaches raw binaries (`tft-notify_<os>_<arch>`, `tft-monitor_linux_<arch>`), `compat.json` and a cosign-signed `checksums.txt`.

**tft-notify** (every user, every machine) comes from mise:

```
mise use -g github:cristiansteib/tft-smarter     # see the dotfiles for the asset pattern
tft-notify agents-install                       # the skill for coding agents
```

**tft-monitor** (the account the board is plugged into) runs as a systemd user service:

```
curl -fsSLo ~/.local/bin/tft-monitor https://github.com/cristiansteib/tft-smarter/releases/latest/download/tft-monitor_linux_amd64
chmod +x ~/.local/bin/tft-monitor
tft-monitor install -port /dev/serial/by-id/usb-Arduino...   # writes ~/.config/systemd/user/tft-monitor.service, enables + starts it
tft-monitor uninstall
```

`install` also runs `loginctl enable-linger`, so it starts at boot.

Updates work the way they do in multiversectl:

- `tft-notify self-update` checks the download against `checksums.txt` and swaps the binary in place. If mise installed it, it runs `mise upgrade` instead. Then it refreshes the agent skill and asks the running monitor to update too.
- The monitor updates itself. A released build checks a couple of minutes after it starts, then every 6 h, and replaces its own binary and restarts in place. It skips a release whose `compat.json` says it cannot drive the board's firmware.
- In a terminal, tft-notify says when a release is out. The check runs in the background at most every 6 h and never with `--json`, so it costs agents nothing. `TFT_NO_UPDATE_CHECK=1` turns all of it off. Builds from source (`dev`) never update.

## Versions and compatibility

Three parts have to match: the firmware, `tft-monitor` and its clients (`tft-notify`, the desktop app). Their contract is in `internal/version`:

- **`Proto`** is the serial protocol. The firmware's `PROTO` (in `src/main.cpp`) has to fall between the monitor's `MinProto` and `Proto`. Bump both when a frame is added or changed.
- **`API`** is the control socket.

```
$ tft-notify status
tft-notify   0.16.0
tft-monitor  0.16.0  drives firmware protocol 1..1, auto-update on: up to date
firmware     v0.16.0  protocol 1  ok
```

`status` exits 6 on a mismatch. The monitor also logs a mismatch and shows it on the screen once. Old firmware means flash it (`mise run flash`); a monitor older than the firmware updates itself.

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
