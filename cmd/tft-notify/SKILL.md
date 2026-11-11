---
name: tft-notify
description: The user has a screen on their desk. Use `tft-notify` to tell them when long work finished or failed, or when you need them to act now and they may not be watching.
---

# tft-notify

```sh
tft-notify -l LEVEL [-s SOURCE] [-a "what to do"] "Short title" "optional body"
```

Levels: `info` (FYI, 20s) · `success` (30s) · `warn` (2m) · `error` (10m) · `action` = you are blocked on the user; blinks until tapped.

More: `-f key=value` rows, `-p 0-100` progress, `--id ID` updates one notice instead of stacking, `-t 90s|0` time on
screen, `--wait --timeout 10m` blocks until tapped (exit 0; 4 expired, 5 timeout), `--json`. Exit 3 = no screen: carry on.
All flags: `tft-notify --help-json`.

- One notice per event, title under ~40 chars, ASCII. Not for routine progress.
- With `action`, still ask in the chat: the screen only gets their attention.

```sh
tft-notify -l success -s claude "Tests pass" "142 passed in 38s"
tft-notify -l action -s claude -a "Answer in the terminal" "Need your OK to push"
```
