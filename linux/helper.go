package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/xjasonlyu/tun2socks/v2/engine"
)

const (
	tunName = "cb0"
	tunAddr = "198.18.0.1/15"
)

const dnsmasqConf = "/etc/NetworkManager/dnsmasq-shared.d/cellbridge.conf"

// runDnsmasq toggles a NetworkManager shared-mode dnsmasq snippet that stops
// the hotspot from advertising this laptop as router/DNS. Without it the
// iPhone would send its own traffic to the laptop, which sends it back to the
// phone. dnsmasq reads it only at startup, so it is switched on just for the
// moment the hotspot comes up.
func runDnsmasq(args []string) {
	if len(args) != 1 {
		log.Fatal("usage: cellbridge dnsmasq on|off")
	}
	switch args[0] {
	case "on":
		body := "# Written by CellBridge while its hotspot starts; removed right after.\n" +
			"# Don't hand out a default gateway or DNS server to hotspot clients.\n" +
			"dhcp-option=3\ndhcp-option=6\n"
		if err := os.WriteFile(dnsmasqConf, []byte(body), 0o644); err != nil {
			log.Fatal(err)
		}
	case "off":
		if err := os.Remove(dnsmasqConf); err != nil && !os.IsNotExist(err) {
			log.Fatal(err)
		}
	}
}

// runHelper runs as root. It brings up the tun device, routes everything
// through it and keeps going until stdin closes or it gets a signal.
func runHelper(args []string) {
	fs := flag.NewFlagSet("helper", flag.ExitOnError)
	proxy := fs.String("proxy", "", "phone SOCKS5 address host:port")
	iface := fs.String("iface", "", "Wi-Fi interface that reaches the phone")
	dns := fs.String("dns", "1.1.1.1,8.8.8.8", "comma-separated DNS servers")
	fs.Parse(args)
	if *proxy == "" || *iface == "" {
		log.Fatal("helper: --proxy and --iface are required")
	}
	if os.Geteuid() != 0 {
		log.Fatal("helper: must run as root")
	}

	// Leftover device from a crashed run would make engine start fail.
	exec.Command("ip", "link", "del", tunName).Run()

	engine.Insert(&engine.Key{
		Proxy:                    "socks5://" + *proxy,
		Device:                   "tun://" + tunName,
		Interface:                *iface,
		MTU:                      1500,
		LogLevel:                 "warn",
		UDPTimeout:               30 * time.Second,
		TCPModerateReceiveBuffer: true,
	})
	engine.Start()

	cleanup := func() {
		exec.Command("resolvectl", "revert", tunName).Run()
		engine.Stop()
		exec.Command("ip", "link", "del", tunName).Run()
	}

	steps := [][]string{
		{"ip", "addr", "replace", tunAddr, "dev", tunName},
		{"ip", "link", "set", tunName, "up"},
		{"ip", "route", "replace", "0.0.0.0/1", "dev", tunName},
		{"ip", "route", "replace", "128.0.0.0/1", "dev", tunName},
	}
	for _, s := range steps {
		if out, err := exec.Command(s[0], s[1:]...).CombinedOutput(); err != nil {
			cleanup()
			log.Fatalf("%s: %v: %s", strings.Join(s, " "), err, out)
		}
	}
	dnsArgs := append([]string{"dns", tunName}, strings.Split(*dns, ",")...)
	exec.Command("resolvectl", dnsArgs...).Run()
	exec.Command("resolvectl", "domain", tunName, "~.").Run()
	exec.Command("resolvectl", "default-route", tunName, "yes").Run()
	exec.Command("resolvectl", "flush-caches").Run()

	fmt.Println("READY")

	done := make(chan struct{}, 2)
	go func() {
		io.Copy(io.Discard, bufio.NewReader(os.Stdin))
		done <- struct{}{}
	}()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	go func() { <-sig; done <- struct{}{} }()
	<-done
	cleanup()
}
