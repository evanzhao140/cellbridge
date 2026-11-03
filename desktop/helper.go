package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/xjasonlyu/tun2socks/v2/engine"
)

const tunAddr = "198.18.0.1/15"

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

	// Undo whatever a crashed previous run left behind.
	cleanupNet(*iface)
	removeTun()

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
		cleanupNet(*iface)
		engine.Stop()
		removeTun()
	}
	if err := setupNet(*iface, strings.Split(*dns, ",")); err != nil {
		cleanup()
		log.Fatal(err)
	}

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
