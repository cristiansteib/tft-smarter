package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

const unitName = "tft-monitor.service"

func unitPath() string {
	dir, _ := os.UserConfigDir()
	return filepath.Join(dir, "systemd", "user", unitName)
}

func systemctl(args ...string) error {
	cmd := exec.Command("systemctl", append([]string{"--user"}, args...)...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}

// installService writes a systemd user unit for this binary and starts it (re-run after upgrading).
func installService(port string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return err
	}
	unit := fmt.Sprintf(`[Unit]
Description=Arduino TFT PC status monitor

[Service]
ExecStart=%s -port %s
Restart=always
RestartSec=3

[Install]
WantedBy=default.target
`, exe, port)
	if err := os.MkdirAll(filepath.Dir(unitPath()), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(unitPath(), []byte(unit), 0o644); err != nil {
		return err
	}
	if err := systemctl("daemon-reload"); err != nil {
		return err
	}
	if err := systemctl("enable", "--now", unitName); err != nil {
		return err
	}
	// optional: keep it running without an open login session / start at boot
	if err := exec.Command("loginctl", "enable-linger").Run(); err != nil {
		fmt.Println("note: could not enable lingering; the service starts at your first login instead")
	}
	fmt.Println("installed", unitPath())
	return nil
}

func uninstallService() error {
	_ = systemctl("disable", "--now", unitName)
	if err := os.Remove(unitPath()); err != nil && !os.IsNotExist(err) {
		return err
	}
	_ = systemctl("daemon-reload")
	fmt.Println("removed", unitName)
	return nil
}
