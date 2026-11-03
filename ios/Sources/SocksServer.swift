import Foundation
import Network

/// Byte/connection counters shared between the relay queue and the UI.
final class Stats {
    private let lock = NSLock()
    private var _up: UInt64 = 0
    private var _down: UInt64 = 0
    private var _active = 0

    func add(up: Int = 0, down: Int = 0) {
        lock.lock(); _up &+= UInt64(up); _down &+= UInt64(down); lock.unlock()
    }
    func sessionDelta(_ d: Int) { lock.lock(); _active += d; lock.unlock() }
    func snapshot() -> (up: UInt64, down: UInt64, active: Int) {
        lock.lock(); defer { lock.unlock() }
        return (_up, _down, _active)
    }
}

/// SOCKS5 server (CONNECT + UDP ASSOCIATE) that listens on Wi-Fi and
/// opens every outbound connection over the cellular interface.
final class SocksServer {
    let queue = DispatchQueue(label: "cellbridge.socks", qos: .userInitiated)
    let stats = Stats()
    /// Port actually bound (0 until the listener is ready).
    private(set) var port: UInt16 = 0
    /// Preferred ports, then any free port; the laptop finds us via Bonjour or by probing these.
    static let ports: [UInt16] = Array(1080...1089) + [0]
    private var portIndex = 0
    var forceCellular = true
    var hotspotOnly = true
    var onState: ((String, Bool) -> Void)?

    private var listener: NWListener?
    private var sessions: [ObjectIdentifier: SocksSession] = [:]

    func start() {
        queue.async { self._start() }
    }

    func stop() {
        queue.async {
            self.listener?.cancel()
            self.listener = nil
            self.port = 0
            for s in self.sessions.values { s.close() }
            self.sessions.removeAll()
        }
    }

    private func _start() {
        guard listener == nil else { return }
        let tcp = NWProtocolTCP.Options()
        tcp.noDelay = true
        let params = NWParameters(tls: nil, tcp: tcp)
        // No interface restriction: a Wi-Fi-bound listener gets torn down (EADDRINUSE)
        // when the phone switches networks. Peers are filtered in accept() instead.
        params.allowLocalEndpointReuse = true
        let want = Self.ports[portIndex]
        do {
            let l = try NWListener(using: params, on: want == 0 ? .any : NWEndpoint.Port(rawValue: want)!)
            l.service = NWListener.Service(name: "CellBridge", type: "_cellbridge._tcp")
            l.stateUpdateHandler = { [weak self] st in
                guard let self else { return }
                switch st {
                case .ready:
                    self.port = l.port?.rawValue ?? want
                    self.onState?("Listening on port \(self.port)", true)
                case .waiting(let e): self.onState?("Waiting for Wi-Fi: \(e.localizedDescription)", true)
                case .failed(let e):
                    self.onState?("Listener failed on \(want): \(e.localizedDescription) – retrying", false)
                    self.retry(after: e)
                case .cancelled: self.onState?("Stopped", false)
                default: break
                }
            }
            l.newConnectionHandler = { [weak self] c in self?.accept(c) }
            listener = l
            l.start(queue: queue)
        } catch {
            onState?("Cannot listen on \(want): \(error.localizedDescription) – retrying", false)
            retry(after: error as? NWError)
        }
    }

    private func retry(after e: NWError?) {
        if case .posix(.EADDRINUSE)? = e, portIndex < Self.ports.count - 1 { portIndex += 1 }
        listener?.stateUpdateHandler = nil
        listener?.cancel()
        listener = nil
        port = 0
        queue.asyncAfter(deadline: .now() + 1) { self._start() }
    }

    private func accept(_ c: NWConnection) {
        if hotspotOnly ? !Self.isHotspotPeer(c.endpoint) : !Self.isLANPeer(c.endpoint) {
            c.cancel(); return
        }
        let s = SocksSession(client: c, server: self)
        sessions[ObjectIdentifier(s)] = s
        stats.sessionDelta(1)
        s.start()
    }

    func ended(_ s: SocksSession) {
        if sessions.removeValue(forKey: ObjectIdentifier(s)) != nil { stats.sessionDelta(-1) }
    }

    /// Outbound parameters: cellular only, so nothing loops back over Wi-Fi.
    func outboundParams(udp: Bool) -> NWParameters {
        let p: NWParameters
        if udp {
            p = NWParameters.udp
        } else {
            let tcp = NWProtocolTCP.Options()
            tcp.noDelay = true
            tcp.connectionTimeout = 15
            p = NWParameters(tls: nil, tcp: tcp)
        }
        if forceCellular {
            p.requiredInterfaceType = .cellular
        } else {
            p.prohibitedInterfaceTypes = [.wifi]
        }
        return p
    }

    /// NetworkManager's shared-mode hotspot hands out 10.42.x.x.
    static func isHotspotPeer(_ ep: NWEndpoint) -> Bool {
        guard let b = ipv4Bytes(ep) else { return false }
        return b[0] == 10 && b[1] == 42
    }

    /// Private LAN ranges (RFC 1918 + link-local); never carrier space like 100.64/10.
    static func isLANPeer(_ ep: NWEndpoint) -> Bool {
        guard let b = ipv4Bytes(ep) else { return false }
        return b[0] == 10 || (b[0] == 172 && (16...31).contains(b[1])) ||
            (b[0] == 192 && b[1] == 168) || (b[0] == 169 && b[1] == 254)
    }

    private static func ipv4Bytes(_ ep: NWEndpoint) -> [UInt8]? {
        guard case let .hostPort(host, _) = ep, case let .ipv4(a) = host else { return nil }
        let b = [UInt8](a.rawValue)
        return b.count == 4 ? b : nil
    }
}

/// One SOCKS5 client connection.
final class SocksSession {
    let client: NWConnection
    unowned let server: SocksServer
    private var remote: NWConnection?
    private var udp: UDPAssociation?
    private var closed = false
    private var eofs = 0

    init(client: NWConnection, server: SocksServer) {
        self.client = client
        self.server = server
    }

    func start() {
        client.stateUpdateHandler = { [weak self] st in
            switch st {
            case .failed, .cancelled: self?.close()
            default: break
            }
        }
        client.start(queue: server.queue)
        read(2) { h in
            guard h[0] == 5 else { return self.close() }
            self.read(Int(h[1])) { _ in
                self.client.send(content: Data([5, 0]), completion: .contentProcessed { _ in })
                self.readRequest()
            }
        }
    }

    func close() {
        if closed { return }
        closed = true
        client.cancel()
        remote?.cancel()
        udp?.cancel()
        server.ended(self)
    }

    private func read(_ n: Int, _ cb: @escaping ([UInt8]) -> Void) {
        if n == 0 { cb([]); return }
        client.receive(minimumIncompleteLength: n, maximumLength: n) { [weak self] data, _, _, _ in
            guard let self, !self.closed else { return }
            guard let data, data.count == n else { return self.close() }
            cb([UInt8](data))
        }
    }

    private func reply(_ rep: UInt8, addr: [UInt8] = [0, 0, 0, 0], port: UInt16 = 0) {
        var b: [UInt8] = [5, rep, 0, 1] + addr
        b += [UInt8(port >> 8), UInt8(port & 0xff)]
        client.send(content: Data(b), completion: .contentProcessed { _ in })
    }

    private func readRequest() {
        read(4) { r in
            guard r[0] == 5 else { return self.close() }
            let cmd = r[1]
            self.readAddress(atyp: r[3]) { host in
                self.read(2) { p in
                    let port = UInt16(p[0]) << 8 | UInt16(p[1])
                    switch cmd {
                    case 1: self.connect(host: host, port: port)
                    case 3: self.associate()
                    default: self.reply(7); self.close()
                    }
                }
            }
        }
    }

    private func readAddress(atyp: UInt8, _ cb: @escaping (NWEndpoint.Host) -> Void) {
        switch atyp {
        case 1: read(4) { a in cb(.ipv4(IPv4Address(Data(a))!)) }
        case 4: read(16) { a in cb(.ipv6(IPv6Address(Data(a))!)) }
        case 3:
            read(1) { l in
                self.read(Int(l[0])) { name in cb(.name(String(decoding: name, as: UTF8.self), nil)) }
            }
        default: reply(8); close()
        }
    }

    // MARK: CONNECT

    private func connect(host: NWEndpoint.Host, port: UInt16) {
        let r = NWConnection(host: host, port: NWEndpoint.Port(rawValue: port)!,
                             using: server.outboundParams(udp: false))
        remote = r
        var replied = false
        r.stateUpdateHandler = { [weak self] st in
            guard let self, !self.closed else { return }
            switch st {
            case .ready:
                if replied { return }
                replied = true
                self.reply(0)
                self.pump(from: self.client, to: r, up: true)
                self.pump(from: r, to: self.client, up: false)
            case .waiting, .failed:
                // .waiting = no usable (cellular) path right now; fail fast.
                if !replied { replied = true; self.reply(4) }
                self.close()
            case .cancelled:
                self.close()
            default: break
            }
        }
        r.start(queue: server.queue)
    }

    private func pump(from: NWConnection, to: NWConnection, up: Bool) {
        from.receive(minimumIncompleteLength: 1, maximumLength: 256 * 1024) { [weak self] data, _, isComplete, error in
            guard let self, !self.closed else { return }
            if let data, !data.isEmpty {
                if up { self.server.stats.add(up: data.count) } else { self.server.stats.add(down: data.count) }
                to.send(content: data, completion: .contentProcessed { [weak self] err in
                    guard let self, !self.closed else { return }
                    if err != nil { return self.close() }
                    if isComplete { self.finish(to) } else { self.pump(from: from, to: to, up: up) }
                })
            } else if isComplete {
                self.finish(to)
            } else if error != nil {
                self.close()
            } else {
                self.pump(from: from, to: to, up: up)
            }
        }
    }

    /// Half-close `to` (send FIN); once both directions are done, tear down.
    private func finish(_ to: NWConnection) {
        to.send(content: nil, contentContext: .defaultStream, isComplete: true, completion: .contentProcessed { _ in })
        eofs += 1
        if eofs >= 2 { close() }
    }

    // MARK: UDP ASSOCIATE

    private func associate() {
        var bind: [UInt8] = [0, 0, 0, 0]
        if case let .hostPort(h, _)? = client.currentPath?.localEndpoint, case let .ipv4(a) = h {
            bind = [UInt8](a.rawValue)
        }
        let assoc = UDPAssociation(server: server)
        udp = assoc
        assoc.start { [weak self] port in
            guard let self, !self.closed else { return }
            guard let port else { self.reply(1); return self.close() }
            self.reply(0, addr: bind, port: port)
            self.drainControl()
        }
    }

    /// The UDP association lives as long as the TCP control connection.
    private func drainControl() {
        client.receive(minimumIncompleteLength: 1, maximumLength: 4096) { [weak self] _, _, isComplete, error in
            guard let self, !self.closed else { return }
            if isComplete || error != nil { self.close() } else { self.drainControl() }
        }
    }
}

/// Relays SOCKS5-encapsulated datagrams between a Wi-Fi client and the internet.
final class UDPAssociation {
    unowned let server: SocksServer
    private var listener: NWListener?
    private var flows: [NWConnection] = []
    private var outs: [String: NWConnection] = [:]
    private var cancelled = false

    init(server: SocksServer) { self.server = server }

    func start(_ ready: @escaping (UInt16?) -> Void) {
        let params = NWParameters.udp
        guard let l = try? NWListener(using: params, on: .any) else { return ready(nil) }
        listener = l
        var reported = false
        l.stateUpdateHandler = { st in
            switch st {
            case .ready:
                if !reported { reported = true; ready(l.port?.rawValue) }
            case .failed:
                if !reported { reported = true; ready(nil) }
            default: break
            }
        }
        l.newConnectionHandler = { [weak self] flow in
            guard let self, !self.cancelled else { flow.cancel(); return }
            self.flows.append(flow)
            flow.start(queue: self.server.queue)
            self.receive(flow)
        }
        l.start(queue: server.queue)
    }

    func cancel() {
        cancelled = true
        listener?.cancel()
        flows.forEach { $0.cancel() }
        outs.values.forEach { $0.cancel() }
        flows.removeAll(); outs.removeAll()
    }

    private func receive(_ flow: NWConnection) {
        flow.receiveMessage { [weak self] data, _, _, error in
            guard let self, !self.cancelled else { return }
            if let data { self.handle([UInt8](data), from: flow) }
            if error == nil { self.receive(flow) }
        }
    }

    private func handle(_ b: [UInt8], from flow: NWConnection) {
        // RSV(2) FRAG(1) ATYP(1) ADDR PORT(2) DATA
        guard b.count > 4, b[2] == 0 else { return }
        var i = 4
        let host: NWEndpoint.Host
        switch b[3] {
        case 1:
            guard b.count >= i + 6 else { return }
            host = .ipv4(IPv4Address(Data(b[i..<i + 4]))!); i += 4
        case 4:
            guard b.count >= i + 18 else { return }
            host = .ipv6(IPv6Address(Data(b[i..<i + 16]))!); i += 16
        case 3:
            let n = Int(b[i]); i += 1
            guard b.count >= i + n + 2 else { return }
            host = .name(String(decoding: b[i..<i + n], as: UTF8.self), nil); i += n
        default: return
        }
        let port = UInt16(b[i]) << 8 | UInt16(b[i + 1]); i += 2
        let header = Data(b[0..<i])
        let payload = Data(b[i...])
        server.stats.add(up: payload.count)

        let key = "\(ObjectIdentifier(flow).hashValue)|\(host)|\(port)"
        let out: NWConnection
        if let o = outs[key] {
            out = o
        } else {
            out = NWConnection(host: host, port: NWEndpoint.Port(rawValue: port)!,
                               using: server.outboundParams(udp: true))
            outs[key] = out
            out.start(queue: server.queue)
            receiveRemote(out, header: header, flow: flow)
        }
        out.send(content: payload, completion: .contentProcessed { _ in })
    }

    private func receiveRemote(_ out: NWConnection, header: Data, flow: NWConnection) {
        out.receiveMessage { [weak self] data, _, _, error in
            guard let self, !self.cancelled else { return }
            if let data, !data.isEmpty {
                self.server.stats.add(down: data.count)
                flow.send(content: header + data, completion: .contentProcessed { _ in })
            }
            if error == nil { self.receiveRemote(out, header: header, flow: flow) }
        }
    }
}
