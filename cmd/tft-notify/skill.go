package main

import (
	_ "embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

//go:embed SKILL.md
var skill string

const skillName = "tft-notify"

// skillDirs are where agents look for skills: Claude Code always, the shared ~/.agents dir when it is in use.
func skillDirs(a *args) ([]string, error) {
	if d := a.vals["dir"]; len(d) > 0 {
		return d, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	dirs := []string{filepath.Join(home, ".claude", "skills")}
	if st, err := os.Stat(filepath.Join(home, ".agents")); err == nil && st.IsDir() {
		dirs = append(dirs, filepath.Join(home, ".agents", "skills"))
	}
	return dirs, nil
}

func cmdAgentsInstall(a *args) error {
	if a.flag("print") {
		fmt.Fprint(stdout, skill)
		return nil
	}
	dirs, err := skillDirs(a)
	if err != nil {
		return err
	}
	var done []string
	for _, d := range dirs {
		dir := filepath.Join(d, skillName)
		f := filepath.Join(dir, "SKILL.md")
		if a.flag("remove") {
			if err := os.Remove(f); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			os.Remove(dir) // only if empty
			done = append(done, f)
			continue
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(f, []byte(skill), 0o644); err != nil {
			return err
		}
		done = append(done, f)
	}
	verb := "installed"
	if a.flag("remove") {
		verb = "removed"
	}
	emit(a, map[string]any{verb: done}, verb+":\n  "+strings.Join(done, "\n  "))
	return nil
}
