# CellBridge

Use an iPhone's cellular data on a Linux laptop **without Personal Hotspot**, over Wi-Fi.

```
apps on laptop ─► cb0 (tun2socks) ─► laptop Wi-Fi AP "CellBridge-XXXX" ─► iPhone app (SOCKS5 :1080) ─► cellular ─► internet
```

* The laptop runs its own WPA2 access point (NetworkManager shared mode). DHCP gives the
  phone no gateway/DNS, so iOS treats it as "No Internet" Wi-Fi and keeps its own traffic on cellular.
* The iOS app is a SOCKS5 server (TCP CONNECT + UDP ASSOCIATE) listening on Wi-Fi only; every
  outbound connection is pinned to the cellular interface.
* The tray app finds the phone, then a root helper creates `cb0`, routes `0.0.0.0/1` + `128.0.0.0/1`
  through it and points systemd-resolved at 1.1.1.1/8.8.8.8 via the tunnel.
* Throughput is limited by Wi-Fi and cellular, not by the relay (the tunnel measured ~200 Mbit/s in tests).

## Linux (`linux/`)

```bash
cd linux && go build -o cellbridge . && install -m755 cellbridge ~/.local/bin/
```

Run `cellbridge`. It adds itself to autostart. Tray menu: **Connect** / Disconnect, live speed,
the hotspot SSID and password, "Create Wi-Fi hotspot" (uncheck if phone and laptop already share a Wi-Fi), auto-connect.
Root steps go through `sudo -n` or, failing that, `pkexec`. Config is in `~/.config/cellbridge/config.json`
and the log is in `~/.cache/cellbridge.log`.

Note: while the hotspot is up, the laptop's Wi-Fi card is busy as the AP, so it leaves your normal Wi-Fi.
Disconnect restores it.

## iOS (`ios/`)

GitHub Actions (`.github/workflows/ios.yml`) builds an **unsigned** `CellBridge.ipa` on every push and
publishes it to the `latest` release. Sideload it with AltStore / SideStore / Sideloadly / Impactor (free Apple ID works;
re-sign every 7 days).

On the phone: Settings → Wi-Fi → join `CellBridge-XXXX` (password in the tray menu) → open CellBridge →
**Start sharing** → allow Local Network. Silent background audio keeps the app alive in the background.
