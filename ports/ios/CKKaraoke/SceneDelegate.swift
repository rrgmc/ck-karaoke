import UIKit

/// Owns the app's one window. Apps built with the current SDK must use the scene
/// life cycle: without a scene manifest the app fails to launch. With one, the
/// system reports foreground and background changes here and no longer calls the
/// application delegate's equivalents.
final class SceneDelegate: UIResponder, UIWindowSceneDelegate {
    var window: UIWindow?

    /// Keeps the app running for a little while after it stops being looked at, so
    /// the session survives a glance at another app.
    private var backgroundTask: UIBackgroundTaskIdentifier = .invalid

    /// Whether the grace period ran out and the session was actually given up.
    /// Only then does the page need putting back in order — see
    /// RootViewController.resumeWeb.
    private var didSuspend = false

    func scene(_ scene: UIScene, willConnectTo session: UISceneSession,
               options connectionOptions: UIScene.ConnectionOptions) {
        guard let windowScene = scene as? UIWindowScene else { return }
        let window = UIWindow(windowScene: windowScene)
        window.rootViewController = RootViewController()
        window.backgroundColor = .white
        window.makeKeyAndVisible()
        self.window = window
    }

    /// iOS has no equivalent of the foreground service the Android app uses, so the
    /// session cannot simply be kept. It is held for the grace period the system
    /// gives — long enough that switching apps and coming straight back does not
    /// drop it — and given up cleanly when that runs out.
    ///
    /// Given up rather than abandoned: the unit serves five clients, and a session
    /// left open holds one of those slots until the device times it out by itself.
    func sceneDidEnterBackground(_ scene: UIScene) {
        guard backgroundTask == .invalid else { return }
        backgroundTask = UIApplication.shared.beginBackgroundTask(withName: "ckkaraoke-session") { [weak self] in
            // Logged because this is the whole point of the grace period and the
            // only way to see it happen: the Go side reports suspending and
            // resuming, but not what asked it to.
            NSLog("ckkaraoke: background time expired, giving up the session")
            self?.didSuspend = true
            Server.suspend()
            self?.endBackgroundTask()
        }
        NSLog("ckkaraoke: entered background, holding the session for now")
    }

    func sceneWillEnterForeground(_ scene: UIScene) {
        NSLog("ckkaraoke: returning to foreground")
        endBackgroundTask()
        // A no-op if the grace period never ran out, which is the common case.
        Server.resume()

        // The page outlived a suspension, so its event stream is dead even though
        // nothing about it says so.
        guard didSuspend else { return }
        didSuspend = false
        (window?.rootViewController as? RootViewController)?.resumeWeb()
    }

    private func endBackgroundTask() {
        guard backgroundTask != .invalid else { return }
        UIApplication.shared.endBackgroundTask(backgroundTask)
        backgroundTask = .invalid
    }
}
