//go:build darwin

package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// dnsBackup remembers the Wi-Fi service's DNS servers so they can be restored,
// even after a crash.
const dnsBackup = "/var/run/cellbridge-dns"

func runDnsmasq([]string) {}

// networkService maps a BSD device (en0) to its network service name (Wi-Fi).
func networkService(iface string) string {
	out, _ := exec.Command("networksetup", "-listnetworkserviceorder").Output()
	name := ""
	for _, l := range strings.Split(string(out), "\n") {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "(") && !strings.HasPrefix(l, "(Hardware") {
			if _, rest, ok := strings.Cut(l, ") "); ok {
				name = rest
			}
		} else if strings.Contains(l, "Device: "+iface+")") {
			return name
		}
	}
	return "Wi-Fi"
}

func run(args ...string) error {
	if out, err := exec.Command(args[0], args[1:]...).CombinedOutput(); err != nil {
		return fmt.Errorf("%s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func setupNet(iface string, dns []string) error {
	if err := run("ifconfig", tunName, "inet", "198.18.0.1", "198.18.0.1", "netmask", "255.254.0.0", "up"); err != nil {
		return err
	}
	for _, n := range []string{"0.0.0.0/1", "128.0.0.0/1"} {
		if err := run("route", "-q", "-n", "add", "-net", n, "-interface", tunName); err != nil {
			return err
		}
	}
	svc := networkService(iface)
	out, _ := exec.Command("networksetup", "-getdnsservers", svc).Output()
	old := "Empty"
	if s := strings.TrimSpace(string(out)); s != "" && !strings.Contains(s, "aren't any") {
		old = strings.Join(strings.Fields(s), " ")
	}
	os.WriteFile(dnsBackup, []byte(svc+"\n"+old+"\n"), 0o600)
	if err := run(append([]string{"networksetup", "-setdnsservers", svc}, dns...)...); err != nil {
		return err
	}
	// Publish the tunnel as a network service in configd's dynamic store. On a
	// network whose DHCP gives no router (the Linux hotspot does that for the
	// iPhone), macOS otherwise has no primary service: apps think they are
	// offline and the DNS servers above are ignored.
	if err := scutil(fmt.Sprintf(`d.init
d.add Addresses * 198.18.0.1
d.add DestAddresses * 198.18.0.1
d.add SubnetMasks * 255.254.0.0
d.add InterfaceName %s
d.add Router 198.18.0.1
set %s/IPv4
d.init
d.add ServerAddresses * %s
d.add SupplementalMatchDomains * ""
set %s/DNS
`, tunName, scKey, strings.Join(dns, " "), scKey)); err != nil {
		return err
	}
	exec.Command("dscacheutil", "-flushcache").Run()
	exec.Command("killall", "-HUP", "mDNSResponder").Run()
	return nil
}

// scKey is our service in the dynamic store; it vanishes on reboot.
const scKey = "State:/Network/Service/com.ozok.cellbridge"

func scutil(script string) error {
	cmd := exec.Command("scutil")
	cmd.Stdin = strings.NewReader(script + "quit\n")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("scutil: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func cleanupNet(string) {
	scutil("remove " + scKey + "/IPv4\nremove " + scKey + "/DNS\n")
	for _, n := range []string{"0.0.0.0/1", "128.0.0.0/1"} {
		exec.Command("route", "-q", "-n", "delete", "-net", n, "-interface", tunName).Run()
	}
	b, err := os.ReadFile(dnsBackup)
	if err != nil {
		return
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) == 2 {
		exec.Command("networksetup", append([]string{"-setdnsservers", lines[0]}, strings.Fields(lines[1])...)...).Run()
		exec.Command("dscacheutil", "-flushcache").Run()
		exec.Command("killall", "-HUP", "mDNSResponder").Run()
	}
	os.Remove(dnsBackup)
}

// utun devices disappear when tun2socks closes them.
func removeTun() {}
