package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"log"
	"math"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"fyne.io/systray"
)

type state int

const (
	stOff state = iota
	stBusy
	stOn
	stErr
)

// icon draws a phone-with-signal glyph tinted for the given state.
func icon(s state) []byte {
	const n = 64
	col := map[state]color.RGBA{
		stOff:  {150, 150, 150, 255},
		stBusy: {240, 170, 30, 255},
		stOn:   {40, 200, 90, 255},
		stErr:  {220, 60, 60, 255},
	}[s]
	img := image.NewRGBA(image.Rect(0, 0, n, n))
	set := func(x, y int) { img.Set(x, y, col) }
	// Phone body (outline) at the left.
	for y := 14; y < 58; y++ {
		for x := 6; x < 30; x++ {
			edge := x < 10 || x >= 26 || y < 18 || y >= 50
			if edge || (y >= 52 && x >= 15 && x < 21) {
				set(x, y)
			}
		}
	}
	// Signal arcs radiating to the upper right.
	cx, cy := 30.0, 34.0
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			dx, dy := float64(x)-cx, cy-float64(y)
			if dx <= 0 || dy < -dx*0.9 || dy > dx*1.6 {
				continue
			}
			r := math.Hypot(dx, dy)
			for _, R := range []float64{12, 22, 32} {
				if math.Abs(r-R) < 2.6 {
					set(x, y)
				}
			}
		}
	}
	var buf bytes.Buffer
	png.Encode(&buf, img)
	return buf.Bytes()
}

type controller struct {
	cfg *Config

	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}

	mStatus, mSpeed, mToggle, mWifi, mHotspot, mAuto *systray.MenuItem
}

func (c *controller) setStatus(s state, msg string) {
	systray.SetIcon(icon(s))
	systray.SetTooltip("CellBridge – " + msg)
	c.mStatus.SetTitle(msg)
	log.Print(msg)
}

func (c *controller) running() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cancel != nil
}

func (c *controller) start() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.cancel, c.done = cancel, make(chan struct{})
	c.mToggle.SetTitle("Disconnect")
	c.mHotspot.Disable()
	go func(done chan struct{}) {
		defer close(done)
		c.loop(ctx)
	}(c.done)
}

func (c *controller) stop() {
	c.mu.Lock()
	cancel, done := c.cancel, c.done
	c.cancel = nil
	c.mu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	<-done
	c.mToggle.SetTitle("Connect")
	c.mHotspot.Enable()
	c.mSpeed.SetTitle("—")
	c.setStatus(stOff, "Disconnected")
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

// loop: bring up hotspot → find phone → run tunnel → on loss, repeat.
func (c *controller) loop(ctx context.Context) {
	iface, err := wifiIface()
	if err != nil {
		c.setStatus(stErr, err.Error())
		<-ctx.Done()
		return
	}
	if c.cfg.Hotspot {
		defer hotspotDown()
	}
	for ctx.Err() == nil {
		if c.cfg.Hotspot && !hotspotActive() {
			c.setStatus(stBusy, "Starting Wi-Fi hotspot "+c.cfg.SSID+"…")
			if err := hotspotUp(iface, c.cfg); err != nil {
				c.setStatus(stErr, "Hotspot failed: "+firstLine(err.Error()))
				if !sleepCtx(ctx, 5*time.Second) {
					return
				}
				continue
			}
		}
		c.setStatus(stBusy, "Waiting for iPhone (join “"+c.ssidHint()+"”, open CellBridge)")
		phone := findPhone(ctx, iface, c.cfg.LastPhone, c.cfg.Port)
		if phone == "" {
			sleepCtx(ctx, 2*time.Second)
			continue
		}
		c.cfg.LastPhone = phone
		c.cfg.save()
		c.setStatus(stBusy, "Found iPhone at "+phone+", starting tunnel…")
		if err := c.tunnel(ctx, iface, phone); err != nil {
			c.setStatus(stErr, firstLine(err.Error()))
			sleepCtx(ctx, 3*time.Second)
		}
	}
}

func (c *controller) ssidHint() string {
	if c.cfg.Hotspot {
		return c.cfg.SSID
	}
	return "the same Wi-Fi"
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(s, "\n")
	if len(s) > 90 {
		s = s[:90] + "…"
	}
	return s
}

// tunnel runs the root helper until ctx ends, the helper dies or the phone
// stops answering.
func (c *controller) tunnel(ctx context.Context, iface, phone string) error {
	addr := net.JoinHostPort(phone, fmt.Sprint(c.cfg.Port))
	cmd := privileged("helper", "--proxy", addr, "--iface", iface)
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("cannot start helper: %w", err)
	}
	exited := make(chan error, 1)
	ready := make(chan bool, 1)
	go func() {
		sc := bufio.NewScanner(stdout)
		ok := false
		for sc.Scan() {
			if strings.TrimSpace(sc.Text()) == "READY" && !ok {
				ok = true
				ready <- true
			}
		}
		if !ok {
			ready <- false
		}
		exited <- cmd.Wait()
	}()
	shutdown := func() {
		stdin.Close() // helper cleans up when stdin closes
		select {
		case <-exited:
		case <-time.After(5 * time.Second):
			cmd.Process.Kill()
		}
	}

	select {
	case ok := <-ready:
		if !ok {
			<-exited
			return fmt.Errorf("tunnel failed: %s", strings.TrimSpace(stderr.String()))
		}
	case <-ctx.Done():
		shutdown()
		return nil
	}

	c.setStatus(stOn, "Connected via iPhone "+phone)
	c.mWifi.SetTitle(c.wifiTitle())
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	lastRx, lastTx, t0 := readCounter("rx_bytes"), readCounter("tx_bytes"), time.Now()
	misses, n := 0, 0
	for {
		select {
		case <-ctx.Done():
			shutdown()
			return nil
		case err := <-exited:
			return fmt.Errorf("tunnel stopped: %v %s", err, strings.TrimSpace(stderr.String()))
		case <-tick.C:
			rx, tx, now := readCounter("rx_bytes"), readCounter("tx_bytes"), time.Now()
			dt := now.Sub(t0).Seconds()
			// tun tx = packets delivered to this machine (download).
			down := float64(tx-lastTx) * 8 / dt / 1e6
			up := float64(rx-lastRx) * 8 / dt / 1e6
			lastRx, lastTx, t0 = rx, tx, now
			c.mSpeed.SetTitle(fmt.Sprintf("↓ %.1f Mbit/s   ↑ %.1f Mbit/s", down, up))
			if n++; n%3 == 0 {
				if isSocks(addr, 2*time.Second) {
					misses = 0
				} else if misses++; misses >= 3 {
					shutdown()
					c.setStatus(stBusy, "Lost iPhone, searching again…")
					return nil
				}
			}
		}
	}
}

func (c *controller) wifiTitle() string {
	if !c.cfg.Hotspot {
		return "Mode: phone & laptop on the same Wi-Fi"
	}
	return fmt.Sprintf("Wi-Fi: %s   password: %s", c.cfg.SSID, c.cfg.Password)
}

func installAutostart() {
	self, err := os.Executable()
	if err != nil {
		return
	}
	dir, _ := os.UserConfigDir()
	p := filepath.Join(dir, "autostart", "cellbridge.desktop")
	body := "[Desktop Entry]\nType=Application\nName=CellBridge\n" +
		"Comment=Use iPhone cellular data over Wi-Fi without Personal Hotspot\n" +
		"Exec=" + self + "\nIcon=network-wireless\nX-GNOME-Autostart-enabled=true\n"
	os.MkdirAll(filepath.Dir(p), 0o755)
	if old, _ := os.ReadFile(p); string(old) != body {
		os.WriteFile(p, []byte(body), 0o644)
	}
}

func runTray() {
	if dir, err := os.UserCacheDir(); err == nil {
		os.MkdirAll(dir, 0o755)
		if f, err := os.OpenFile(filepath.Join(dir, "cellbridge.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
			log.SetOutput(io.MultiWriter(os.Stderr, f))
		}
	}
	c := &controller{cfg: loadConfig()}
	installAutostart()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	go func() { <-sig; systray.Quit() }()

	systray.Run(func() { c.ready() }, func() { c.stop() })
}

func (c *controller) ready() {
	systray.SetIcon(icon(stOff))
	systray.SetTitle("")
	systray.SetTooltip("CellBridge")
	c.mStatus = systray.AddMenuItem("Disconnected", "")
	c.mStatus.Disable()
	c.mSpeed = systray.AddMenuItem("—", "")
	c.mSpeed.Disable()
	systray.AddSeparator()
	c.mToggle = systray.AddMenuItem("Connect", "Share the iPhone's cellular data")
	c.mWifi = systray.AddMenuItem(c.wifiTitle(), "Join this network on the iPhone")
	c.mWifi.Disable()
	systray.AddSeparator()
	c.mHotspot = systray.AddMenuItemCheckbox("Create Wi-Fi hotspot on this laptop", "Uncheck if the phone and laptop already share a Wi-Fi network", c.cfg.Hotspot)
	c.mAuto = systray.AddMenuItemCheckbox("Connect automatically on startup", "", c.cfg.AutoConnect)
	mLog := systray.AddMenuItem("Open log", "")
	mQuit := systray.AddMenuItem("Quit", "")

	if c.cfg.AutoConnect {
		c.start()
	}
	go func() {
		for {
			select {
			case <-c.mToggle.ClickedCh:
				if c.running() {
					go c.stop()
				} else {
					c.start()
				}
			case <-c.mHotspot.ClickedCh:
				c.cfg.Hotspot = !c.cfg.Hotspot
				if c.cfg.Hotspot {
					c.mHotspot.Check()
				} else {
					c.mHotspot.Uncheck()
				}
				c.cfg.save()
				c.mWifi.SetTitle(c.wifiTitle())
			case <-c.mAuto.ClickedCh:
				c.cfg.AutoConnect = !c.cfg.AutoConnect
				if c.cfg.AutoConnect {
					c.mAuto.Check()
				} else {
					c.mAuto.Uncheck()
				}
				c.cfg.save()
			case <-mLog.ClickedCh:
				dir, _ := os.UserCacheDir()
				exec.Command("xdg-open", filepath.Join(dir, "cellbridge.log")).Start()
			case <-mQuit.ClickedCh:
				systray.Quit()
				return
			}
		}
	}()
}
