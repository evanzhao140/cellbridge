import AVFoundation

/// Plays silence so iOS keeps the app (and the proxy) running in the background.
final class BackgroundKeeper {
    private let engine = AVAudioEngine()
    private let player = AVAudioPlayerNode()
    private var buffer: AVAudioPCMBuffer?
    private(set) var running = false

    init() {
        NotificationCenter.default.addObserver(
            forName: AVAudioSession.interruptionNotification, object: nil, queue: .main
        ) { [weak self] note in
            guard let self, self.running,
                  let raw = note.userInfo?[AVAudioSessionInterruptionTypeKey] as? UInt,
                  AVAudioSession.InterruptionType(rawValue: raw) == .ended else { return }
            self.play()
        }
        NotificationCenter.default.addObserver(
            forName: .AVAudioEngineConfigurationChange, object: engine, queue: .main
        ) { [weak self] _ in
            guard let self, self.running else { return }
            self.play()
        }
    }

    func start() {
        running = true
        let session = AVAudioSession.sharedInstance()
        try? session.setCategory(.playback, options: [.mixWithOthers])
        try? session.setActive(true)

        let format = AVAudioFormat(standardFormatWithSampleRate: 44100, channels: 2)!
        if buffer == nil {
            let b = AVAudioPCMBuffer(pcmFormat: format, frameCapacity: 44100)!
            b.frameLength = b.frameCapacity
            for ch in 0..<Int(format.channelCount) {
                b.floatChannelData![ch].initialize(repeating: 0, count: Int(b.frameLength))
            }
            buffer = b
            engine.attach(player)
            engine.connect(player, to: engine.mainMixerNode, format: format)
        }
        play()
    }

    func stop() {
        running = false
        player.stop()
        engine.stop()
        try? AVAudioSession.sharedInstance().setActive(false, options: .notifyOthersOnDeactivation)
    }

    private func play() {
        guard let buffer else { return }
        try? AVAudioSession.sharedInstance().setActive(true)
        if !engine.isRunning { try? engine.start() }
        player.stop()
        player.scheduleBuffer(buffer, at: nil, options: .loops)
        player.play()
    }
}
