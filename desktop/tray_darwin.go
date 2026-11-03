//go:build darwin

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
)

const launchAgentLabel = "com.ozok.cellbridge"

// installAutostart registers a LaunchAgent that starts the menu-bar app
// (disconnected) at login.
func installAutostart() {
	home, _ := os.UserHomeDir()
	p := filepath.Join(home, "Library", "LaunchAgents", launchAgentLabel+".plist")
	body := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key><string>` + launchAgentLabel + `</string>
	<key>ProgramArguments</key>
	<array><string>` + selfPath() + `</string><string>--login</string></array>
	<key>RunAtLoad</key><true/>
	<key>LimitLoadToSessionType</key><string>Aqua</string>
</dict>
</plist>
`
	os.MkdirAll(filepath.Dir(p), 0o755)
	if old, _ := os.ReadFile(p); string(old) != body {
		os.WriteFile(p, []byte(body), 0o644)
	}
}

// The .app lives in /Applications / Launchpad already.
func installMenuEntry() {}

// wantsConnect: opening the app connects; the login LaunchAgent passes --login.
func wantsConnect() bool { return !slices.Contains(os.Args[1:], "--login") }

func notify(msg string) {
	exec.Command("osascript", "-e", "display notification "+strconv.Quote(msg)+` with title "CellBridge"`).Run()
}

func openFile(p string) { exec.Command("open", "-a", "Console", p).Start() }
