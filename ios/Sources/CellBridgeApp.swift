import SwiftUI
import UIKit

enum KeepAlive: String, CaseIterable, Identifiable {
    case location, audio, both
    var id: String { rawValue }
    var label: String {
        switch self {
        case .location: return "Location"
        case .audio: return "Silent audio"
        case .both: return "Both"
        }
    }
}

final class AppModel: ObservableObject {
    @Published var running = false
    @Published var status = "Stopped"
    @Published var wifiIP = "—"
    @Published var active = 0
    @Published var port: UInt16 = 0
    @Published var upRate = 0.0
    @Published var downRate = 0.0
    @Published var upTotal: UInt64 = 0
    @Published var downTotal: UInt64 = 0
    @Published var hotspotOnly: Bool {
        didSet { UserDefaults.standard.set(hotspotOnly, forKey: "hotspotOnly"); server.hotspotOnly = hotspotOnly }
    }
    @Published var forceCellular: Bool {
        didSet { UserDefaults.standard.set(forceCellular, forKey: "forceCellular"); server.forceCellular = forceCellular }
    }
    @Published var keepAlive: KeepAlive {
        didSet { UserDefaults.standard.set(keepAlive.rawValue, forKey: "keepAlive"); applyKeepers() }
    }
    /// What is actually keeping the app alive right now (shown in the UI).
    @Published var keepAliveNote = ""

    let server = SocksServer()
    private let audio = BackgroundKeeper()
    private let location = LocationKeeper()
    private var timer: Timer?
    private var last: (up: UInt64, down: UInt64, t: Date) = (0, 0, Date())

    init() {
        let d = UserDefaults.standard
        d.register(defaults: ["hotspotOnly": true, "forceCellular": true, "autoStart": true, "keepAlive": KeepAlive.location.rawValue])
        hotspotOnly = d.bool(forKey: "hotspotOnly")
        forceCellular = d.bool(forKey: "forceCellular")
        keepAlive = KeepAlive(rawValue: d.string(forKey: "keepAlive") ?? "") ?? .location
        server.hotspotOnly = hotspotOnly
        server.forceCellular = forceCellular
        server.onState = { [weak self] msg, _ in
            DispatchQueue.main.async { self?.status = msg }
        }
        location.onAuthorizationChange = { [weak self] in self?.applyKeepers() }
        timer = Timer.scheduledTimer(withTimeInterval: 1, repeats: true) { [weak self] _ in self?.tick() }
        Signing.scheduleReminders()
        if d.bool(forKey: "autoStart") { start() }
    }

    func start() {
        running = true
        UserDefaults.standard.set(true, forKey: "autoStart")
        applyKeepers()
        server.start()
    }

    func stop() {
        running = false
        UserDefaults.standard.set(false, forKey: "autoStart")
        server.stop()
        applyKeepers()
        status = "Stopped"
    }

    /// Starts/stops the background keep-alives for the chosen mode. If location
    /// is denied, silent audio takes over so sharing still survives the lock screen.
    private func applyKeepers() {
        guard running else {
            location.stop()
            if audio.running { audio.stop() }
            keepAliveNote = ""
            return
        }
        let wantLocation = keepAlive != .audio
        var wantAudio = keepAlive != .location
        if wantLocation {
            if !location.running { location.start() }
        } else if location.running {
            location.stop()
        }
        if wantLocation && location.denied { wantAudio = true }
        if wantAudio {
            if !audio.running { audio.start() }
        } else if audio.running {
            audio.stop()
        }
        switch (wantLocation && location.authorized, wantAudio) {
        case (true, true): keepAliveNote = "Location + silent audio"
        case (true, false): keepAliveNote = "Low-power location (blue arrow in status bar)"
        case (false, true): keepAliveNote = wantLocation ? "Location not allowed – using silent audio" : "Silent audio"
        case (false, false): keepAliveNote = "Waiting for location permission…"
        }
    }

    private func tick() {
        let s = server.stats.snapshot()
        let now = Date()
        let dt = max(now.timeIntervalSince(last.t), 0.001)
        upRate = Double(s.up &- last.up) * 8 / dt / 1_000_000
        downRate = Double(s.down &- last.down) * 8 / dt / 1_000_000
        last = (s.up, s.down, now)
        upTotal = s.up
        downTotal = s.down
        active = s.active
        port = server.port
        wifiIP = Self.wifiAddress() ?? "not on Wi-Fi"
    }

    static func wifiAddress() -> String? {
        var ifaddr: UnsafeMutablePointer<ifaddrs>?
        guard getifaddrs(&ifaddr) == 0, let first = ifaddr else { return nil }
        defer { freeifaddrs(ifaddr) }
        for p in sequence(first: first, next: { $0.pointee.ifa_next }) {
            let ifa = p.pointee
            guard let sa = ifa.ifa_addr, sa.pointee.sa_family == UInt8(AF_INET),
                  String(cString: ifa.ifa_name) == "en0" else { continue }
            var host = [CChar](repeating: 0, count: Int(NI_MAXHOST))
            getnameinfo(sa, socklen_t(sa.pointee.sa_len), &host, socklen_t(host.count), nil, 0, NI_NUMERICHOST)
            return String(cString: host)
        }
        return nil
    }
}

@main
struct CellBridgeApp: App {
    @StateObject private var model = AppModel()
    var body: some Scene {
        WindowGroup { ContentView().environmentObject(model) }
    }
}

struct ContentView: View {
    @EnvironmentObject var m: AppModel

    var body: some View {
        NavigationView {
            Form {
                Section {
                    Button(action: { m.running ? m.stop() : m.start() }) {
                        Text(m.running ? "Stop sharing" : "Start sharing")
                            .font(.title2.bold())
                            .frame(maxWidth: .infinity)
                            .padding(.vertical, 8)
                    }
                    .tint(m.running ? .red : .green)
                    .buttonStyle(.borderedProminent)
                    .listRowBackground(Color.clear)
                }
                if let expiry = Signing.expiry {
                    Section(footer: Text("Sideloading another app with a new certificate can stop CellBridge from opening earlier – re-sideload it if that happens.")) {
                        HStack {
                            Text("Signing expires")
                            Spacer()
                            Text(expiry.formatted(date: .abbreviated, time: .shortened))
                                .foregroundColor(expiry.timeIntervalSinceNow < 2 * 86_400 ? .orange : .secondary)
                        }
                    }
                }
                Section("Status") {
                    row("State", m.status)
                    row("Wi-Fi IP", m.wifiIP)
                    row("Proxy", m.port == 0 ? "not listening" : "SOCKS5 :\(m.port)")
                    row("Connections", "\(m.active)")
                }
                Section("Throughput") {
                    row("↓ to laptop", String(format: "%.1f Mbit/s", m.downRate))
                    row("↑ from laptop", String(format: "%.1f Mbit/s", m.upRate))
                    row("Total", "\(fmt(m.downTotal)) ↓  \(fmt(m.upTotal)) ↑")
                }
                Section(header: Text("Keep running in background"),
                        footer: Text("Works with the screen locked. Location uses cell towers, not GPS, and is never stored or sent anywhere. Swiping the app away stops sharing.")) {
                    Picker("Keep alive", selection: $m.keepAlive) {
                        ForEach(KeepAlive.allCases) { Text($0.label).tag($0) }
                    }
                    .pickerStyle(.segmented)
                    if !m.keepAliveNote.isEmpty { row("Active", m.keepAliveNote) }
                }
                Section(footer: Text("Join the laptop's “ozok internet” Wi-Fi (Settings → Wi-Fi). It has no internet — that's expected; the phone keeps using cellular. You can lock the phone; just don't swipe CellBridge away.")) {
                    Toggle("Only accept laptop hotspot (10.42.x.x)", isOn: $m.hotspotOnly)
                    Toggle("Force cellular for outbound", isOn: $m.forceCellular)
                }
            }
            .navigationTitle("CellBridge")
        }
        .navigationViewStyle(.stack)
    }

    private func row(_ k: String, _ v: String) -> some View {
        HStack { Text(k); Spacer(); Text(v).foregroundColor(.secondary).monospacedDigit() }
    }

    private func fmt(_ b: UInt64) -> String {
        ByteCountFormatter.string(fromByteCount: Int64(b), countStyle: .binary)
    }
}
