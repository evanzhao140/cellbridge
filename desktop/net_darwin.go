//go:build darwin

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	tunName = "utun233"
	// macOS can't run a WPA2 access point from the command line, so the Mac
	// always joins an existing network the iPhone is on (e.g. the Linux
	// laptop's "ozok internet" hotspot).
	canHotspot = false
)

// The Cocoa status item must be driven from the main OS thread.
func init() { runtime.LockOSThread() }

// wifiIface returns the BSD device of the Wi-Fi hardware port (usually en0).
func wifiIface() (string, error) {
	out, err := exec.Command("networksetup", "-listallhardwareports").Output()
	if err != nil {
		return "", fmt.Errorf("networksetup: %w", err)
	}
	port := ""
	for _, l := range strings.Split(string(out), "\n") {
		if v, ok := strings.CutPrefix(l, "Hardware Port: "); ok {
			port = v
		} else if v, ok := strings.CutPrefix(l, "Device: "); ok && (port == "Wi-Fi" || port == "AirPort") {
			return strings.TrimSpace(v), nil
		}
	}
	return "", errors.New("no Wi-Fi device found")
}

func selfPath() string {
	self, _ := os.Executable()
	if p, err := filepath.EvalSymlinks(self); err == nil {
		return p
	}
	return self
}

// privileged runs this binary as root via sudo. The first time, it asks for
// an admin password once and installs a sudoers rule that allows exactly
// "<this binary> helper ..." without a password.
func privileged(args ...string) *exec.Cmd {
	self := selfPath()
	if os.Geteuid() == 0 {
		return exec.Command(self, args...)
	}
	if exec.Command("sudo", append([]string{"-n", "-l", self}, args...)...).Run() != nil {
		if err := installSudoers(self); err != nil {
			notify("Couldn't get admin rights: " + firstLine(err.Error()))
		}
	}
	return exec.Command("sudo", append([]string{"-n", self}, args...)...)
}

func installSudoers(self string) error {
	u, err := user.Current()
	if err != nil {
		return err
	}
	esc := strings.NewReplacer(`\`, `\\`, ` `, `\ `, `,`, `\,`, `:`, `\:`, `=`, `\=`).Replace(self)
	rule := fmt.Sprintf("# CellBridge: let %s start the tunnel helper without a password.\n%s ALL=(root) NOPASSWD: %s helper *\n", u.Username, u.Username, esc)
	f, err := os.CreateTemp("", "cellbridge-sudoers-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	f.WriteString(rule)
	f.Close()
	sh := fmt.Sprintf("/usr/sbin/visudo -cf '%s' && /usr/bin/install -m 0440 -o root -g wheel '%s' /etc/sudoers.d/cellbridge", f.Name(), f.Name())
	script := fmt.Sprintf(`do shell script "%s" with administrator privileges with prompt "CellBridge needs permission once to route this Mac's traffic through your iPhone."`,
		strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(sh))
	if out, err := exec.Command("osascript", "-e", script).CombinedOutput(); err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func hotspotUp(string, *Config) error { return errors.New("hotspot not supported on macOS") }
func hotspotDown()                    {}
func hotspotActive() bool             { return false }

// browseMDNS: the subnet probe finds the phone quickly enough on macOS.
func browseMDNS(context.Context, string) []string { return nil }

// neighbors lists resolved IPv4 ARP entries on iface.
func neighbors(iface string) []string {
	out, _ := exec.Command("arp", "-an", "-i", iface).Output()
	var ips []string
	for _, l := range strings.Split(string(out), "\n") {
		// ? (10.42.0.26) at 1a:1d:1c:16:10:71 on en0 ifscope [ethernet]
		f := strings.Fields(l)
		if len(f) >= 4 && f[3] != "(incomplete)" && strings.HasPrefix(f[1], "(") {
			ips = append(ips, strings.Trim(f[1], "()"))
		}
	}
	return ips
}

// tunBytes returns bytes delivered to this Mac (down) and sent by it (up).
func tunBytes() (down, up uint64) {
	out, _ := exec.Command("netstat", "-I", tunName, "-b", "-n").Output()
	lines := strings.Split(string(out), "\n")
	if len(lines) < 2 {
		return 0, 0
	}
	hdr := strings.Fields(lines[0])
	col := func(name string) int {
		for i, h := range hdr {
			if h == name {
				return i
			}
		}
		return -1
	}
	ib, ob := col("Ibytes"), col("Obytes")
	for _, l := range lines[1:] {
		f := strings.Fields(l)
		// The <Link#N> row has an empty Address column, so it is one field short.
		if len(f) == len(hdr)-1 && strings.HasPrefix(f[2], "<Link") && ib > 0 && ob > 0 {
			fmt.Sscan(f[ib-1], &down)
			fmt.Sscan(f[ob-1], &up)
			return
		}
	}
	return 0, 0
}
