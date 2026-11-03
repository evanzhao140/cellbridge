package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type Config struct {
	SSID        string `json:"ssid"`
	Password    string `json:"password"`
	Hotspot     bool   `json:"hotspot"`      // create our own AP (vs. reuse current Wi-Fi)
	AutoConnect bool   `json:"auto_connect"` // connect when the tray starts
	LastPhone   string `json:"last_phone"`   // "ip:port"
	Band        string `json:"band"`         // AP band that last worked: "a" or "bg"
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
	c := &Config{Hotspot: canHotspot}
	if b, err := os.ReadFile(configPath()); err == nil {
		json.Unmarshal(b, c)
	}
	if c.SSID == "" {
		c.SSID = "ozok internet"
	}
	if !canHotspot {
		c.Hotspot = false
	}
	if c.Password == "" {
		c.Password = randString("abcdefghjkmnpqrstuvwxyz23456789", 12)
	}
	c.save()
	return c
}

func (c *Config) save() {
	os.MkdirAll(filepath.Dir(configPath()), 0o700)
	b, _ := json.MarshalIndent(c, "", "  ")
	os.WriteFile(configPath(), b, 0o600)
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

// candidates lists addresses to probe: Bonjour adverts, the last phone,
// ARP neighbours on every fallback port, then every fallback port across our /24.
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
	for _, ip := range neighbors(iface) {
		for _, p := range phonePorts {
			if a := net.JoinHostPort(ip, fmt.Sprint(p)); inNet(a) {
				add(a)
			}
		}
	}
	base := self.Mask(net.CIDRMask(24, 32))
	for _, p := range phonePorts {
		for i := 1; i < 255; i++ {
			if a := net.JoinHostPort(net.IPv4(base[0], base[1], base[2], byte(i)).String(), fmt.Sprint(p)); inNet(a) {
				add(a)
			}
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
	sem := make(chan struct{}, 256)
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
