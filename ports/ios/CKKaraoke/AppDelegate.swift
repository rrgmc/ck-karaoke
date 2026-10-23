import UIKit

/// The window and the foreground and background handling live in SceneDelegate.
@main
final class AppDelegate: UIResponder, UIApplicationDelegate {
    func application(_ application: UIApplication,
                     didFinishLaunchingWithOptions options: [UIApplication.LaunchOptionsKey: Any]?) -> Bool {
        true
    }

    /// Best effort only: the system allows a few seconds here and does not promise
    /// to call this at all, so the session is asked to close rather than waited on.
    func applicationWillTerminate(_ application: UIApplication) {
        Server.suspend()
    }
}
