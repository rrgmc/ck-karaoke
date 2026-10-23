import Foundation
import UIKit

/// Owns the Go server running inside this process.
///
/// The Android app's counterpart forks the same server as a child process and
/// scrapes its stdout for the port. iOS forbids exec, so the server is linked in
/// as a static archive and the port comes back through the C bridge instead.
enum Server {
    /// Where the databases live.
    ///
    /// Application Support rather than Documents: the catalogue is a 2.2 MB copy
    /// of what the device already holds, not something the user made, and
    /// Documents is backed up to iCloud and can be exposed in the Files app.
    static var dataDirectory: URL {
        let base = FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask)[0]
        let dir = base.appendingPathComponent("ckkaraoke", isDirectory: true)
        try? FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        return dir
    }

    /// The name the device shows beside songs queued from this phone.
    ///
    /// The device takes it from the session, not the command, so one app is one
    /// name. `UIDevice.name` has returned the model rather than the user's chosen
    /// name since iOS 16 unless the app is specially entitled, so there is nothing
    /// better to default to than the model itself.
    static var deviceName: String {
        Settings.name ?? UIDevice.current.model
    }

    /// Starts the server. Returns false if one is already running.
    @discardableResult
    static func start() -> Bool {
        passTimeZone()

        let dir = dataDirectory.path
        let name = deviceName
        let addr = Settings.deviceAddress ?? ""
        return dir.withCString { d in
            name.withCString { n in
                addr.withCString { a in
                    // These buffers live only until the closures return, so the
                    // bridge must copy all three into Go strings before it hands
                    // control back. It does; reading one later, from a goroutine,
                    // is a use-after-free that presents as a garbled address.
                    CKStart(UnsafeMutablePointer(mutating: d),
                                 UnsafeMutablePointer(mutating: n),
                                 UnsafeMutablePointer(mutating: a)) == 0
                }
            }
        }
    }

    /// Tells the server which time zone this device is in.
    ///
    /// Go does not work it out for itself on iOS: `time`'s `initLocal` is, in
    /// full, `localLoc = *UTC`, and there is no platform zoneinfo source at all.
    /// Left alone the server believes it is in UTC, which after 21:00 in Brazil
    /// means a favorites backup is named with tomorrow's date, and every log
    /// line is an hour or three out from the console timestamp beside it.
    ///
    /// Passed through the environment rather than as another `CKStart`
    /// argument, which keeps the C surface small. The bridge reads it with `getenv` rather than `os.Getenv`,
    /// deliberately: the Go runtime copies the environment when it initialises,
    /// and in a c-archive that has already happened by the time any Swift runs,
    /// so its copy would never see this.
    ///
    /// The offset does the work; the name is carried so the zone reads as
    /// "America/Sao_Paulo" rather than "-03" wherever it is printed. Set on
    /// every start, so a device that has crossed a zone picks up the new one on
    /// the next restart.
    private static func passTimeZone() {
        let zone = TimeZone.current
        setenv("CK_TZ", zone.identifier, 1)
        setenv("CK_TZ_OFFSET", String(zone.secondsFromGMT()), 1)
    }

    /// The port the server is listening on, or nil until it is.
    static var port: Int? {
        let p = Int(CKPort())
        return p == 0 ? nil : p
    }

    /// Why the server stopped, or nil if it has not.
    ///
    /// The string belongs to Go and must not be freed here.
    static var failure: String? {
        guard let c = CKError() else { return nil }
        return String(cString: c)
    }

    static var url: URL? {
        guard let port else { return nil }
        return URL(string: "http://127.0.0.1:\(port)/")
    }

    /// How much of the song catalogue has arrived, or nil when nothing is being
    /// downloaded. Only a first run downloads anything.
    static var progress: Int? {
        let p = Int(CKProgress())
        return p < 0 ? nil : p
    }

    static func suspend() { CKSuspend() }
    static func resume() { CKResume() }

    /// Blocks for up to six seconds, so never call it on the main thread.
    static func stop() { CKStop() }

    /// Waits for the server to come up, calling `ready` or `failed` on the main
    /// queue.
    ///
    /// There is no deadline while the server is still working. The first run
    /// downloads the whole song catalogue, which can take twenty seconds, and a
    /// fixed deadline would report failure on a server doing exactly what it is
    /// supposed to. The only failure
    /// signal that means anything is the server saying so.
    static func waitUntilReady(progress: @escaping (Int?) -> Void,
                               ready: @escaping (URL) -> Void,
                               failed: @escaping (String) -> Void) {
        DispatchQueue.global(qos: .userInitiated).async {
            var lastReported: Int??
            while true {
                if let url {
                    DispatchQueue.main.async { ready(url) }
                    return
                }
                if let failure {
                    DispatchQueue.main.async { failed(failure) }
                    return
                }
                // Only on a change: this loop runs at 10Hz and the download takes
                // upwards of twenty seconds.
                let now = Self.progress
                if lastReported == nil || lastReported! != now {
                    lastReported = .some(now)
                    DispatchQueue.main.async { progress(now) }
                }
                Thread.sleep(forTimeInterval: 0.1)
            }
        }
    }
}

/// The few things worth remembering between launches.
enum Settings {
    private static let addressKey = "device_address"
    private static let nameKey = "queue_name"

    /// A device address typed in by hand, used instead of sweeping for one.
    static var deviceAddress: String? {
        get { value(forKey: addressKey) }
        set { set(newValue, forKey: addressKey) }
    }

    /// An override for the name the device shows beside queued songs.
    static var name: String? {
        get { value(forKey: nameKey) }
        set { set(newValue, forKey: nameKey) }
    }

    private static func value(forKey key: String) -> String? {
        guard let s = UserDefaults.standard.string(forKey: key) else { return nil }
        let trimmed = s.trimmingCharacters(in: .whitespaces)
        return trimmed.isEmpty ? nil : trimmed
    }

    private static func set(_ value: String?, forKey key: String) {
        let trimmed = value?.trimmingCharacters(in: .whitespaces) ?? ""
        if trimmed.isEmpty {
            UserDefaults.standard.removeObject(forKey: key)
        } else {
            UserDefaults.standard.set(trimmed, forKey: key)
        }
    }
}
