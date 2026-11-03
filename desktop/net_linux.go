//go:build linux

package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"time"
)

const (
	conName    = "CellBridge"
	tunName    = "cb0"
	canHotspot = true
)

func nmcli(args ...string) (string, error) {
	out, err := exec.Command("nmcli", args...).CombinedOutput()
	s := strings.TrimSpace(string(out))
	if err != nil {
		return s, fmt.Errorf("nmcli %s: %s", strings.Join(args, " "), s)
	}
	return s, nil
}

// wifiIface returns the first Wi-Fi device NetworkManager knows about.
func wifiIface() (string, error) {
	out, err := nmcli("-t", "-f", "DEVICE,TYPE", "device")
	if err != nil {
		return "", err
	}
	for _, l := range strings.Split(out, "\n") {
		if f := strings.Split(l, ":"); len(f) == 2 && f[1] == "wifi" {
			return f[0], nil
		}
	}
	return "", errors.New("no Wi-Fi device found")
}

// privileged builds a command that re-runs this binary as root.
func privileged(args ...string) *exec.Cmd {
	self, _ := os.Executable()
	if os.Geteuid() == 0 {
		return exec.Command(self, args...)
	}
	if exec.Command("sudo", "-n", "true").Run() == nil {
		return exec.Command("sudo", append([]string{"-n", self}, args...)...)
	}
	return exec.Command("pkexec", append([]string{self}, args...)...)
}

// hotspotUp creates/updates the CellBridge AP profile and activates it,
// trying 5 GHz first and falling back to 2.4 GHz.
func hotspotUp(iface string, c *Config) error {
	if _, err := nmcli("-t", "connection", "show", conName); err != nil {
		if _, err := nmcli("connection", "add", "type", "wifi", "ifname", iface,
			"con-name", conName, "autoconnect", "no", "ssid", c.SSID); err != nil {
			return err
		}
	}
	base := []string{"connection", "modify", conName,
		"connection.interface-name", iface,
		"802-11-wireless.ssid", c.SSID,
		"802-11-wireless.mode", "ap",
		"ipv4.method", "shared",
		"ipv6.method", "disabled",
		"wifi-sec.key-mgmt", "wpa-psk",
		"wifi-sec.proto", "rsn",
		"wifi-sec.pairwise", "ccmp",
		"wifi-sec.group", "ccmp",
		"wifi-sec.psk", c.Password,
		"wifi-sec.pmf", "disable", // plain WPA2-PSK; NM's WPA3/SAE AP mode is flaky with iPhones
	}
	if _, err := nmcli(base...); err != nil {
		return err
	}

	privileged("dnsmasq", "on").Run()
	defer privileged("dnsmasq", "off").Run()

	// 2.4 GHz only by default: this card (and many others) refuses a 5 GHz AP,
	// and each refusal costs ~25 s waiting for NetworkManager's timeout.
	// Set "band": "a" in the config to try 5 GHz first.
	bands := []string{"bg"}
	if c.Band == "a" {
		bands = []string{"a", "bg"}
	}
	var lastErr error
	for _, band := range bands {
		if _, err := nmcli("connection", "modify", conName, "802-11-wireless.band", band); err != nil {
			return err
		}
		if _, err := nmcli("--wait", "25", "connection", "up", conName); err == nil {
			if c.Band != band {
				c.Band = band
				c.save()
			}
			return nil
		} else {
			lastErr = err
		}
	}
	return lastErr
}

func hotspotDown() {
	nmcli("connection", "down", conName)
}

func hotspotActive() bool {
	out, _ := nmcli("-t", "-f", "NAME", "connection", "show", "--active")
	for _, l := range strings.Split(out, "\n") {
		if l == conName {
			return true
		}
	}
	return false
}

// browseMDNS asks avahi for CellBridge Bonjour adverts on iface ("ip:port").
func browseMDNS(ctx context.Context, iface string) []string {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	out, _ := exec.CommandContext(ctx, "avahi-browse", "-rpt", "_cellbridge._tcp").Output()
	var addrs []string
	for _, l := range strings.Split(string(out), "\n") {
		// =;iface;IPv4;name;type;domain;host;address;port;txt
		f := strings.Split(l, ";")
		if len(f) >= 9 && f[0] == "=" && f[1] == iface && f[2] == "IPv4" {
			addrs = append(addrs, net.JoinHostPort(f[7], f[8]))
		}
	}
	return addrs
}

// neighbors lists IPv4 ARP entries on iface.
func neighbors(iface string) []string {
	b, err := os.ReadFile("/proc/net/arp")
	if err != nil {
		return nil
	}
	var out []string
	for _, l := range strings.Split(string(b), "\n")[1:] {
		f := strings.Fields(l)
		if len(f) >= 6 && f[5] == iface && f[2] != "0x0" {
			out = append(out, f[0])
		}
	}
	return out
}

func readCounter(name string) uint64 {
	b, err := os.ReadFile("/sys/class/net/" + tunName + "/statistics/" + name)
	if err != nil {
		return 0
	}
	var v uint64
	fmt.Sscan(string(b), &v)
	return v
}

// tunBytes returns bytes delivered to this machine (down) and sent by it (up).
// On a tun device the kernel "receives" what tun2socks writes back to it.
func tunBytes() (down, up uint64) {
	return readCounter("rx_bytes"), readCounter("tx_bytes")
}
