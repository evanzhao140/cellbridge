import CoreLocation

/// Keeps the app running in the background with continuous, coarse location
/// updates. Three-kilometre accuracy is served from cell towers/Wi-Fi, so the
/// GPS stays off; the fixes themselves are ignored.
final class LocationKeeper: NSObject, CLLocationManagerDelegate {
    private let manager = CLLocationManager()
    private(set) var running = false
    /// Called on the main queue when the user grants or denies location.
    var onAuthorizationChange: (() -> Void)?

    override init() {
        super.init()
        manager.delegate = self
        manager.desiredAccuracy = kCLLocationAccuracyThreeKilometers
        manager.distanceFilter = CLLocationDistanceMax
        manager.pausesLocationUpdatesAutomatically = false
        manager.activityType = .other
    }

    var authorized: Bool {
        let s = manager.authorizationStatus
        return s == .authorizedAlways || s == .authorizedWhenInUse
    }

    var denied: Bool {
        let s = manager.authorizationStatus
        return s == .denied || s == .restricted
    }

    func start() {
        running = true
        if manager.authorizationStatus == .notDetermined {
            // "While Using" is enough: updates started in the foreground keep
            // running in the background (with the blue status-bar indicator).
            manager.requestAlwaysAuthorization()
            return
        }
        begin()
    }

    func stop() {
        running = false
        manager.stopUpdatingLocation()
        manager.allowsBackgroundLocationUpdates = false
    }

    private func begin() {
        guard running, authorized else { return }
        manager.allowsBackgroundLocationUpdates = true
        manager.showsBackgroundLocationIndicator = true
        manager.startUpdatingLocation()
    }

    func locationManagerDidChangeAuthorization(_ manager: CLLocationManager) {
        if running { begin() }
        DispatchQueue.main.async { self.onAuthorizationChange?() }
    }

    func locationManager(_ manager: CLLocationManager, didUpdateLocations locations: [CLLocation]) {}
    func locationManager(_ manager: CLLocationManager, didFailWithError error: Error) {}
}
