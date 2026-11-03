package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const conName = "CellBridge"

type Config struct {
	SSID        string `json:"ssid"`
	Password    string `json:"password"`
	Hotspot     bool   `json:"hotspot"`      // create our own AP (vs. reuse current Wi-Fi)
	AutoConnect bool   `json:"auto_connect"` // connect when the tray starts
	LastPhone   string `json:"last_phone"` // "ip:port"
	Band        string `json:"band"`       // AP band that last worked: "a" or "bg"
}

func configPath() string {
	dir, _ := os.UserConfigDir()
	return filepath.Join(dir, "cellbridge", "config.json")
}

func randString(alphabet string, n int) string {
	b := make([]byte, n)
	for i := range b {
		k, _ := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		b[i] = alphabet[k.Int64()]
	}
	return string(b)
}

func loadConfig() *Config {
	c := &Config{Hotspot: true}
	if b, err := os.ReadFile(configPath()); err == nil {
		json.Unmarshal(b, c)
	}
	if c.SSID == "" {
		c.SSID = "ozok internet"
	}
	if c.Password == "" {
		c.Password = randString("0123456789", 8) // easy to type on a phone
	}
	c.save()
	return c
}

func (c *Config) save() {
	os.MkdirAll(filepath.Dir(configPath()), 0o700)
	b, _ := json.MarshalIndent(c, "", "  ")
	os.WriteFile(configPath(), b, 0o600)
}

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

	// Try the band that worked last time first; many cards refuse a 5 GHz AP.
	bands := []string{"a", "bg"}
	if c.Band == "bg" {
		bands = []string{"bg", "a"}
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

// ifaceNet returns the interface's IPv4 address and network.
func ifaceNet(iface string) (net.IP, *net.IPNet, error) {
	ifc, err := net.InterfaceByName(iface)
	if err != nil {
		return nil, nil, err
	}
	addrs, _ := ifc.Addrs()
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil {
			return n.IP.To4(), n, nil
		}
	}
	return nil, nil, fmt.Errorf("%s has no IPv4 address", iface)
}

// isSocks checks that addr speaks SOCKS5 without auth.
func isSocks(addr string, timeout time.Duration) bool {
	c, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return false
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(timeout))
	if _, err := c.Write([]byte{5, 1, 0}); err != nil {
		return false
	}
	b := make([]byte, 2)
	if _, err := c.Read(b); err != nil {
		return false
	}
	return b[0] == 5 && b[1] == 0
}

// phonePorts mirrors the iOS app's listener fallback list.
var phonePorts = func() []int {
	p := make([]int, 0, 10)
	for i := 1080; i <= 1089; i++ {
		p = append(p, i)
	}
	return p
}()

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

// candidates lists addresses to probe: Bonjour adverts, the last phone,
// ARP neighbours on every fallback port, then port 1080 across our /24.
func candidates(ctx context.Context, iface, last string) []string {
	self, n, err := ifaceNet(iface)
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	add := func(addr string) {
		if !seen[addr] {
			seen[addr] = true
			out = append(out, addr)
		}
	}
	inNet := func(addr string) bool {
		h, _, err := net.SplitHostPort(addr)
		ip := net.ParseIP(h)
		return err == nil && ip != nil && n.Contains(ip) && !ip.Equal(self)
	}
	for _, a := range browseMDNS(ctx, iface) {
		if inNet(a) {
			add(a)
		}
	}
	if inNet(last) {
		add(last)
	}
	if b, err := os.ReadFile("/proc/net/arp"); err == nil {
		for _, l := range strings.Split(string(b), "\n")[1:] {
			f := strings.Fields(l)
			if len(f) >= 6 && f[5] == iface && f[2] != "0x0" {
				for _, p := range phonePorts {
					if a := net.JoinHostPort(f[0], fmt.Sprint(p)); inNet(a) {
						add(a)
					}
				}
			}
		}
	}
	base := self.Mask(net.CIDRMask(24, 32))
	for i := 1; i < 255; i++ {
		if a := net.JoinHostPort(net.IPv4(base[0], base[1], base[2], byte(i)).String(), "1080"); inNet(a) {
			add(a)
		}
	}
	return out
}

// findPhone probes candidates in parallel and returns the first SOCKS5 "ip:port".
func findPhone(ctx context.Context, iface, last string) string {
	cands := candidates(ctx, iface, last)
	if len(cands) == 0 {
		return ""
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	found := make(chan string, 1)
	sem := make(chan struct{}, 64)
	var wg sync.WaitGroup
	for _, addr := range cands {
		wg.Add(1)
		go func(addr string) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()
			if ctx.Err() == nil && isSocks(addr, 600*time.Millisecond) {
				select {
				case found <- addr:
					cancel()
				default:
				}
			}
		}(addr)
	}
	go func() { wg.Wait(); cancel() }()
	<-ctx.Done()
	select {
	case a := <-found:
		return a
	default:
		return ""
	}
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
