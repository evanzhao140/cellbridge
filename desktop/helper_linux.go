//go:build linux

package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
)

const dnsmasqConf = "/etc/NetworkManager/dnsmasq-shared.d/cellbridge.conf"

// runDnsmasq toggles a NetworkManager shared-mode dnsmasq snippet that stops
// the hotspot from advertising this laptop as router/DNS to the iPhone.
// Without it the iPhone would send its own traffic to the laptop, which sends
// it back to the phone. dnsmasq reads it only at startup, so it is switched on just for the
// moment the hotspot comes up.
func runDnsmasq(args []string) {
	if len(args) != 1 {
		log.Fatal("usage: cellbridge dnsmasq on|off")
	}
	switch args[0] {
	case "on":
		body := "# Written by CellBridge while its hotspot starts; removed right after.\n" +
			"# The iPhone gets no gateway/DNS so it keeps its own traffic on cellular.\n" +
			"# Everyone else (e.g. a Mac) gets this laptop as gateway and is NATed\n" +
			"# into the phone tunnel.\n" +
			"dhcp-name-match=set:phone,iPhone*\ndhcp-name-match=set:phone,iPad*\n" +
			"dhcp-option=tag:phone,3\ndhcp-option=tag:phone,6\n"
		if err := os.WriteFile(dnsmasqConf, []byte(body), 0o644); err != nil {
			log.Fatal(err)
		}
	case "off":
		if err := os.Remove(dnsmasqConf); err != nil && !os.IsNotExist(err) {
			log.Fatal(err)
		}
	}
}

// setupNet addresses the tun device, routes everything through it and points
// systemd-resolved at the given DNS servers via the tunnel.
func setupNet(iface string, dns []string) error {
	steps := [][]string{
		{"ip", "addr", "replace", tunAddr, "dev", tunName},
		{"ip", "link", "set", tunName, "up"},
		{"ip", "route", "replace", "0.0.0.0/1", "dev", tunName},
		{"ip", "route", "replace", "128.0.0.0/1", "dev", tunName},
	}
	for _, s := range steps {
		if out, err := exec.Command(s[0], s[1:]...).CombinedOutput(); err != nil {
			return fmt.Errorf("%s: %v: %s", strings.Join(s, " "), err, out)
		}
	}
	exec.Command("resolvectl", append([]string{"dns", tunName}, dns...)...).Run()
	exec.Command("resolvectl", "domain", tunName, "~.").Run()
	exec.Command("resolvectl", "default-route", tunName, "yes").Run()
	exec.Command("resolvectl", "flush-caches").Run()
	return nil
}

func cleanupNet(iface string) {
	if _, err := os.Stat("/sys/class/net/" + tunName); err == nil {
		exec.Command("resolvectl", "revert", tunName).Run()
	}
}

func removeTun() {
	exec.Command("ip", "link", "del", tunName).Run()
}
