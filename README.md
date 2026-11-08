# tft-smarter

PC status dashboard for an Arduino Mega 2560 with a 3.5" ILI9486 TFT shield (480x320).

Parametric desktop enclosure: see [enclosure/README.md](enclosure/README.md)
for the Blender project, printable parts, dimensions, and fit-check procedure.
Parts and shopping links: [BOM.md](BOM.md).

- `src/main.cpp` – firmware (PlatformIO, MCUFRIEND_kbv): CPU/RAM/GPU arc gauges, oscilloscope-style load history, network, disk/VRAM/temperature bars, and a split edge bar for Claude Code weekly (W) and 5-hour session (S) usage.
- `cmd/tft-monitor` – Go program that runs on the PC and streams stats over USB serial (`S,...` every 0.25 s, `C,...` Claude/Docker data every ~2 s).

## Protocol

Binary frames, `0xAA 0x55 | type | len | payload | crc8` (CRC-8, poly 0x07) — see `cmd/tft-monitor/protocol.go`.
The Mega drops any frame with a bad CRC and reports `ok=N bad=M` back every 5 s (`TFT_VERBOSE=1 tft-monitor` prints it).

## Info panel (bottom band)

The PC lays out and rotates pages (disks, top processes, Docker, system pressure/NVMe/GPU power/ping/est. energy since boot)
and sends them as small `cell`/`band` frames; the Mega only draws what changed. Alerts (disk full, temperatures, memory/swap,
load, unhealthy containers, no internet…) take over the band with hysteresis. Push your own message:

```
tft-monitor notify -sev warn -ttl 60 "build finished"     # sev: info|warn|crit
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
mise run flash      # upload firmware (stop the monitor first)
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
