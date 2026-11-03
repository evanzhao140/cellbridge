//go:build linux

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
)

func installAutostart() {
	self, err := os.Executable()
	if err != nil {
		return
	}
	dir, _ := os.UserConfigDir()
	p := filepath.Join(dir, "autostart", "cellbridge.desktop")
	body := "[Desktop Entry]\nType=Application\nName=CellBridge\n" +
		"Comment=Use iPhone cellular data over Wi-Fi without Personal Hotspot\n" +
		"Exec=" + self + "\nIcon=network-wireless\nX-GNOME-Autostart-enabled=true\n"
	os.MkdirAll(filepath.Dir(p), 0o755)
	if old, _ := os.ReadFile(p); string(old) != body {
		os.WriteFile(p, []byte(body), 0o644)
	}
}

// installMenuEntry adds CellBridge to the application menu (Whisker → Internet).
func installMenuEntry() {
	self, err := os.Executable()
	if err != nil {
		return
	}
	home, _ := os.UserHomeDir()
	p := filepath.Join(home, ".local", "share", "applications", "cellbridge.desktop")
	body := "[Desktop Entry]\nType=Application\nName=CellBridge\nGenericName=iPhone cellular sharing\n" +
		"Comment=Use iPhone cellular data over Wi-Fi without Personal Hotspot\n" +
		"Exec=" + self + " --connect\nIcon=network-wireless\nTerminal=false\nCategories=Network;\n" +
		"Keywords=iphone;hotspot;tether;cellular;\n"
	os.MkdirAll(filepath.Dir(p), 0o755)
	if old, _ := os.ReadFile(p); string(old) != body {
		os.WriteFile(p, []byte(body), 0o644)
	}
}

// wantsConnect: the menu entry passes --connect; login autostart doesn't.
func wantsConnect() bool { return slices.Contains(os.Args[1:], "--connect") }

func notify(msg string) {
	exec.Command("notify-send", "-i", "network-wireless", "CellBridge", msg).Run()
}

func openFile(p string) { exec.Command("xdg-open", p).Start() }
