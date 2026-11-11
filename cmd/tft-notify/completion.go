package main

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/cristiansteib/tft-smarter/internal/notify"
	"github.com/cristiansteib/tft-smarter/internal/version"
)

// Shell completion, the way multiversectl does it: `completion` installs for your shell (after asking), `completion
// zsh` prints the script, and an installed script records the version that wrote it so a newer tft-notify rewrites
// it. The scripts are thin: they ask `tft-notify __complete WORDS...` what fits, which reads the same flag spec as
// the parser, so they never fall behind the CLI, and offers the ids of the notices on screen.

var shells = []string{"bash", "zsh", "fish"}

const (
	versionKey = "# tft-notify-completion-version: "
	rcBegin    = "# >>> tft-notify completion >>>"
	rcEnd      = "# <<< tft-notify completion <<<"
)

var scripts = map[string]string{
	"bash": `_tft_notify() {
  local IFS=$'\n'
  COMPREPLY=($(tft-notify __complete "${COMP_WORDS[@]:1:COMP_CWORD}" 2>/dev/null | cut -f1))
}
complete -o default -F _tft_notify tft-notify
`,
	"zsh": `#compdef tft-notify
_tft_notify() {
  local -a items
  local line
  for line in "${(@f)$(tft-notify __complete "${(@)words[2,CURRENT]}" 2>/dev/null)}"; do
    [[ -n $line ]] && items+=("${line%%	*}:${${line#*	}//:/\\:}")
  done
  (( ${#items} )) && _describe -t values 'tft-notify' items || _files
}
compdef _tft_notify tft-notify
`,
	"fish": `function __tft_notify_complete
  set -l words (commandline -opc)
  set -e words[1]
  tft-notify __complete $words (commandline -ct) 2>/dev/null
end
complete -c tft-notify -f -a '(__tft_notify_complete)'
`,
}

func cmdCompletion(a *args) error {
	if len(a.pos) == 1 {
		s, ok := scripts[a.pos[0]]
		if !ok {
			return usageErr{"completion: bash, zsh or fish, not " + a.pos[0]}
		}
		fmt.Fprint(stdout, s)
		return nil
	}
	if len(a.pos) > 1 {
		return usageErr{"usage: " + a.cmd.Usage}
	}
	sh := a.str("shell")
	if sh == "" {
		sh = filepath.Base(os.Getenv("SHELL"))
		if !slices.Contains(shells, sh) {
			return usageErr{fmt.Sprintf("cannot tell your shell from $SHELL (%q): pass --shell bash, zsh or fish", os.Getenv("SHELL"))}
		}
	}
	t, err := target(sh)
	if err != nil {
		return err
	}
	if a.flag("uninstall") {
		removed := os.Remove(t.file) == nil
		if t.rc != "" {
			if b, err := os.ReadFile(t.rc); err == nil && strings.Contains(string(b), rcBegin) {
				if err := os.WriteFile(t.rc, []byte(dropBlock(string(b))), 0o644); err != nil {
					return err
				}
				removed = true
			}
		}
		emit(a, map[string]any{"removed": removed, "shell": sh}, map[bool]string{true: "removed " + sh + " completions", false: "no " + sh + " completions were installed"}[removed])
		return nil
	}
	have := installedVersion(t.file)
	if have == version.Get() && rcPresent(t) {
		emit(a, map[string]any{"shell": sh, "file": t.file, "installed": false}, sh+" completions are up to date ("+t.file+")")
		return nil
	}
	if !a.flag("yes") {
		note := ""
		if t.rc != "" {
			note = " and add a block to " + t.rc + " that loads it"
		}
		if !terminal(os.Stdin) {
			return usageErr{"no terminal to ask on: pass --yes to install, or print the script: tft-notify completion " + sh}
		}
		fmt.Fprintf(os.Stderr, "This will write %s%s.\nInstall %s completions? [Y/n] ", t.file, note, sh)
		ans, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		if ans = strings.ToLower(strings.TrimSpace(ans)); ans != "" && ans != "y" && ans != "yes" {
			emit(a, map[string]any{"installed": false}, "nothing changed")
			return nil
		}
	}
	if err := writeScript(t); err != nil {
		return err
	}
	if t.rc != "" {
		if err := upsertBlock(t); err != nil {
			return err
		}
	}
	hint := "open a new shell to use them"
	if sh == "bash" {
		hint = "bash loads these through the bash-completion package; " + hint
	}
	emit(a, map[string]any{"shell": sh, "file": t.file, "installed": true}, "installed "+sh+" completions ("+t.file+"); "+hint)
	return nil
}

type compTarget struct{ shell, file, rc string }

func target(sh string) (compTarget, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return compTarget{}, err
	}
	env := func(k, def string) string {
		if v := os.Getenv(k); v != "" {
			return v
		}
		return def
	}
	data := env("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	conf := env("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	switch sh {
	case "bash": // bash-completion loads this directory on demand
		return compTarget{sh, filepath.Join(data, "bash-completion", "completions", "tft-notify"), ""}, nil
	case "fish":
		return compTarget{sh, filepath.Join(conf, "fish", "completions", "tft-notify.fish"), ""}, nil
	case "zsh": // nothing puts a user directory on $fpath, so the startup file sources it
		return compTarget{sh, filepath.Join(data, "tft-notify", "completions", "tft-notify.zsh"), filepath.Join(env("ZDOTDIR", home), ".zshrc")}, nil
	}
	return compTarget{}, usageErr{"no completions for " + sh}
}

func installedVersion(file string) string {
	b, err := os.ReadFile(file)
	if err != nil {
		return ""
	}
	line, _, _ := strings.Cut(string(b), "\n")
	if !strings.HasPrefix(line, versionKey) {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(line, versionKey))
}

func writeScript(t compTarget) error {
	if err := os.MkdirAll(filepath.Dir(t.file), 0o755); err != nil {
		return err
	}
	return os.WriteFile(t.file, []byte(versionKey+version.Get()+"\n"+scripts[t.shell]), 0o644)
}

func rcPresent(t compTarget) bool {
	if t.rc == "" {
		return true
	}
	b, err := os.ReadFile(t.rc)
	return err == nil && strings.Contains(string(b), rcBegin)
}

// upsertBlock adds the loader to the startup file, or replaces the one an earlier install left.
func upsertBlock(t compTarget) error {
	b, err := os.ReadFile(t.rc)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	text := dropBlock(string(b))
	if text != "" && !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	block := rcBegin + "\nif [ -f \"" + t.file + "\" ]; then\n" +
		"  (( $+functions[compdef] )) || { autoload -Uz compinit && compinit; }\n" +
		"  source \"" + t.file + "\"\nfi\n" + rcEnd + "\n"
	return os.WriteFile(t.rc, []byte(text+block), 0o644)
}

func dropBlock(text string) string {
	i := strings.Index(text, rcBegin)
	if i < 0 {
		return text
	}
	j := strings.Index(text[i:], rcEnd)
	if j < 0 {
		return text
	}
	end := i + j + len(rcEnd)
	if end < len(text) && text[end] == '\n' {
		end++
	}
	before := strings.TrimRight(text[:i], "\n")
	if before != "" {
		before += "\n"
	}
	return before + text[end:]
}

// refreshCompletions rewrites a script an older tft-notify installed. It never installs one nobody asked for.
func refreshCompletions() {
	for _, sh := range shells {
		if t, err := target(sh); err == nil {
			if have := installedVersion(t.file); have != "" && have != version.Get() {
				writeScript(t)
			}
		}
	}
}

// ---- __complete: what fits the word being typed, "value\tdescription" per line ----

func complete(words []string) []string {
	if len(words) == 0 {
		words = []string{""}
	}
	cur, done := words[len(words)-1], words[:len(words)-1]
	c := commands[0]
	explicit := false
	if len(done) > 0 {
		if f := find(done[0]); f != nil {
			c, explicit = f, true
		}
	}
	var out []string
	add := func(v, d string) {
		if strings.HasPrefix(v, cur) {
			out = append(out, v+"\t"+d)
		}
	}
	// the value of the flag before it
	if len(done) > 0 {
		prev := done[len(done)-1]
		for _, o := range c.Flags {
			if o.Type == "bool" || (prev != "--"+o.Name && (o.Short == "" || prev != "-"+o.Short)) {
				continue
			}
			switch o.Name {
			case "level":
				for _, l := range notify.Levels {
					add(l.Name, l.Use)
				}
			case "view":
				for _, v := range notify.Views {
					add(v, "view")
				}
			case "color":
				names := make([]string, 0, len(notify.Colors))
				for n := range notify.Colors {
					names = append(names, n)
				}
				sort.Strings(names)
				for _, n := range names {
					add(n, notify.Colors[n])
				}
			case "ttl", "timeout":
				for _, d := range []string{"30s", "2m", "10m", "1h", "0"} {
					add(d, map[bool]string{true: "until tapped / no limit", false: d}[d == "0"])
				}
			case "id":
				out = append(out, noticeIDs(cur)...)
			case "shell":
				for _, s := range shells {
					add(s, "shell")
				}
			}
			return out
		}
	}
	if strings.HasPrefix(cur, "-") {
		for _, o := range c.Flags {
			add("--"+o.Name, o.Help)
		}
		if !explicit || c == commands[0] {
			add("--help-json", "everything as JSON, for agents")
		}
		return out
	}
	if !explicit && len(done) == 0 {
		for _, cm := range commands {
			add(cm.Name, cm.Summary)
		}
		return out
	}
	switch c.Name {
	case "get", "wait", "dismiss":
		return noticeIDs(cur)
	case "completion":
		if len(positional(c, done[1:])) == 0 {
			for _, s := range shells {
				add(s, "print the "+s+" script")
			}
		}
	}
	return out
}

// positional drops the flags (and their values) from words.
func positional(c *command, words []string) []string {
	var out []string
	for i := 0; i < len(words); i++ {
		w := words[i]
		if !strings.HasPrefix(w, "-") || w == "-" {
			out = append(out, w)
			continue
		}
		for _, o := range c.Flags {
			if (w == "--"+o.Name || w == "-"+o.Short) && o.Type != "bool" {
				i++
			}
		}
	}
	return out
}

// noticeIDs: the notices on screen, asked of the daemon with a short timeout so a slow one never stalls the shell.
func noticeIDs(cur string) []string {
	var r struct{ Active []notify.Notice }
	if notify.CallTimeout(notify.Request{Cmd: "notices"}, &r, 500*time.Millisecond) != nil {
		return nil
	}
	var out []string
	for _, n := range r.Active {
		if strings.HasPrefix(n.ID, cur) {
			out = append(out, n.ID+"\t"+n.Level+": "+n.Title)
		}
	}
	return out
}
