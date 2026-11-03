import SwiftUI
import UIKit

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

    let server = SocksServer()
    private let keeper = BackgroundKeeper()
    private var timer: Timer?
    private var last: (up: UInt64, down: UInt64, t: Date) = (0, 0, Date())

    init() {
        let d = UserDefaults.standard
        d.register(defaults: ["hotspotOnly": true, "forceCellular": true, "autoStart": true])
        hotspotOnly = d.bool(forKey: "hotspotOnly")
        forceCellular = d.bool(forKey: "forceCellular")
        server.hotspotOnly = hotspotOnly
        server.forceCellular = forceCellular
        server.onState = { [weak self] msg, _ in
            DispatchQueue.main.async { self?.status = msg }
        }
        timer = Timer.scheduledTimer(withTimeInterval: 1, repeats: true) { [weak self] _ in self?.tick() }
        if d.bool(forKey: "autoStart") { start() }
    }

    func start() {
        running = true
        UserDefaults.standard.set(true, forKey: "autoStart")
        keeper.start()
        server.start()
        UIApplication.shared.isIdleTimerDisabled = true
    }

    func stop() {
        running = false
        UserDefaults.standard.set(false, forKey: "autoStart")
        server.stop()
        keeper.stop()
        UIApplication.shared.isIdleTimerDisabled = false
        status = "Stopped"
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
                Section(footer: Text("Join the laptop's “CellBridge-…” Wi-Fi (Settings → Wi-Fi). It has no internet — that's expected; the phone keeps using cellular. Leave this app open or in the background.")) {
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
