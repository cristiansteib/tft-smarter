---
name: tft-notify
description: Put a message on the user's desk screen (a small display by their PC) with the `tft-notify` CLI. Use it when the user may not be watching the terminal - to report that long work finished or failed, or to get their attention when you need them to act now (approve, answer, unblock). Not for routine progress chatter.
---

# tft-notify: the user's desk screen

`tft-notify` shows a notice full screen on a 480x320 display next to the user's PC until it expires or they tap it.

```sh
tft-notify -l LEVEL "Short title" "optional body"
```

| level     | when                                               | stays        |
|-----------|----------------------------------------------------|--------------|
| `info`    | FYI                                                | 20 s         |
| `success` | finished fine                                      | 30 s         |
| `warn`    | worth a look soon                                  | 2 min        |
| `error`   | something failed                                   | 10 min       |
| `action`  | the user must act now (approve, answer, unblock); blinks | until tapped |

Flags: `-a "what to do"` · `-s SOURCE` (e.g. your name or the repo) · `-f key=value` (repeat, ≤6) · `-p 0-100` progress ·
`--id ID` (send again with the same id to update one notice instead of stacking) · `-t 90s|5m|0` (0 = until tapped) ·
`-c pink|#RRGGBB` · `--view simple|detail` · `--json` (JSON output) · `--wait --timeout 10m` (block until tapped:
exit 0 seen, 4 expired, 5 timeout). Everything: `tft-notify --help-json`. Exit 3 = no screen running: carry on without it.

Rules
- Title says what happened in under ~40 chars; the body adds the one detail that matters. Plain ASCII renders best.
- `action` only when you are blocked on the user, and still ask in the chat: the screen only gets their attention.
- One notice per event; use `--id` for updates instead of sending many.

```sh
tft-notify -l success -s claude "Tests pass" "142 passed in 38s"
tft-notify -l error -s claude -f job=lint -f branch=main "CI failed" "eslint: 3 errors in src/app"
tft-notify -l action -s claude -a "Answer in the terminal" "Need your OK to push to main"
tft-notify --id build -p 60 "Building firmware"
```
