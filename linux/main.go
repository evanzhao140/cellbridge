// Command cellbridge is a system-tray app that shares an iPhone's cellular
// data with this laptop without Personal Hotspot.
//
// The laptop runs its own Wi-Fi access point; the iPhone joins it and runs a
// SOCKS5 proxy (the CellBridge iOS app) whose outbound connections use
// cellular. A tun2socks device here routes all laptop traffic into that proxy.
package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "helper":
			runHelper(os.Args[2:])
			return
		case "dnsmasq":
			runDnsmasq(os.Args[2:])
			return
		case "-h", "--help", "help":
			fmt.Println("usage: cellbridge            run the tray app\n" +
				"       cellbridge helper ... (internal, root) run the tun2socks tunnel\n" +
				"       cellbridge dnsmasq on|off (internal, root)")
			return
		}
	}
	runTray()
}
