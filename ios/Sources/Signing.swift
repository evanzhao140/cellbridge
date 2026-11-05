import Foundation
import UserNotifications

/// Sideloaded builds are signed with a 7-day profile; once it expires (or the
/// certificate is revoked by signing another app) iOS refuses to launch the app.
enum Signing {
    /// Expiration date from the embedded provisioning profile, if any.
    static let expiry: Date? = {
        guard let url = Bundle.main.url(forResource: "embedded", withExtension: "mobileprovision"),
              let raw = try? Data(contentsOf: url),
              let text = String(data: raw, encoding: .isoLatin1),
              let start = text.range(of: "<?xml"),
              let end = text.range(of: "</plist>"),
              let xml = String(text[start.lowerBound..<end.upperBound]).data(using: .isoLatin1),
              let plist = try? PropertyListSerialization.propertyList(from: xml, format: nil) as? [String: Any]
        else { return nil }
        return plist["ExpirationDate"] as? Date
    }()

    /// Reminds the user a day and an hour before the app stops opening.
    static func scheduleReminders() {
        guard let expiry else { return }
        let center = UNUserNotificationCenter.current()
        center.requestAuthorization(options: [.alert, .sound]) { granted, _ in
            guard granted else { return }
            let ids = ["signing-1d", "signing-1h"]
            center.removePendingNotificationRequests(withIdentifiers: ids)
            for (id, before) in zip(ids, [86_400.0, 3_600.0]) {
                let when = expiry.addingTimeInterval(-before)
                guard when > Date() else { continue }
                let content = UNMutableNotificationContent()
                content.title = "CellBridge expires soon"
                content.body = "Re-sideload CellBridge before \(expiry.formatted(date: .abbreviated, time: .shortened)) or it will stop opening."
                let trigger = UNTimeIntervalNotificationTrigger(timeInterval: when.timeIntervalSinceNow, repeats: false)
                center.add(UNNotificationRequest(identifier: id, content: content, trigger: trigger))
            }
        }
    }
}
