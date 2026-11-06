import Foundation
import UIKit

/// Sends favorites to the other app on this device, CK Karaoke or CK Queue
/// Remote, and receives what it sends.
///
/// The two apps keep separate favorites. The sync and backup pages offer a
/// ckpeer: link when the other app is installed:
///
/// - `ckpeer:folder?code=<code>` sends one folder's code, the digits a QR code
///   holds. It travels in the other app's URL: `<scheme>://favorites?kind=folder&code=<code>`.
/// - `ckpeer:backup` sends everything. The backup is read from this app's own
///   server and put on a named pasteboard, which only apps of the same team
///   can read, and the URL carries `kind=backup`.
///
/// The receiving app opens a page that asks before it adds anything, and the
/// import only adds.
enum Peer {
    /// What the other app sent, until the web view is up to show it.
    enum Incoming {
        case folder(String)
        case backup(String)
    }

    static var pending: Incoming?

    private static let pasteboard = UIPasteboard.Name("com.rrgmc.ck.favorites")

    /// The other app's URL scheme and name, from CKPeerScheme and CKPeerName
    /// in this target's Info.plist.
    private static var scheme: String? {
        Bundle.main.object(forInfoDictionaryKey: "CKPeerScheme") as? String
    }

    private static var name: String {
        Bundle.main.object(forInfoDictionaryKey: "CKPeerName") as? String ?? ""
    }

    /// Whether the other app is installed. LSApplicationQueriesSchemes lists
    /// both schemes, which canOpenURL requires.
    private static var installed: Bool {
        guard let scheme, let url = URL(string: "\(scheme)://") else { return false }
        return UIApplication.shared.canOpenURL(url)
    }

    /// Tells the server the other app's name when it is installed, so the
    /// pages offer to send to it. Passed through the environment like the
    /// backend; see Server.passBackend.
    static func passPeer() {
        if installed {
            setenv("CK_PEER_APP", name, 1)
        } else {
            unsetenv("CK_PEER_APP")
        }
    }

    // MARK: - Receiving

    /// Takes a URL opened by the other app. Returns false if it is not one.
    @discardableResult
    static func receive(_ url: URL) -> Bool {
        guard url.host == "favorites",
              let items = URLComponents(url: url, resolvingAgainstBaseURL: false)?.queryItems
        else { return false }
        switch items.first(where: { $0.name == "kind" })?.value {
        case "folder":
            guard let code = items.first(where: { $0.name == "code" })?.value, !code.isEmpty else { return false }
            pending = .folder(code)
        case "backup":
            guard let board = UIPasteboard(name: pasteboard, create: false),
                  let xml = board.string, !xml.isEmpty
            else {
                NSLog("ckkaraoke: favorites were sent, but the pasteboard is empty")
                return false
            }
            board.items = []
            pending = .backup(xml)
        default:
            return false
        }
        return true
    }

    /// Opens the page for what was sent, in `web`, and says whether there was
    /// anything. A backup is posted to the server first, which holds it until
    /// the page confirms it.
    static func deliver(to web: PageLoader, port: Int) -> Bool {
        guard let incoming = pending else { return false }
        pending = nil
        let base = "http://127.0.0.1:\(port)"
        switch incoming {
        case .folder(let code):
            var c = URLComponents(string: "\(base)/favorites/sync/incoming")!
            c.queryItems = [URLQueryItem(name: "code", value: code)]
            web.loadPage(c.url!)
        case .backup(let xml):
            var request = URLRequest(url: URL(string: "\(base)/favorites/backup/incoming")!)
            request.httpMethod = "POST"
            request.setValue("application/xml; charset=utf-8", forHTTPHeaderField: "Content-Type")
            request.httpBody = Data(xml.utf8)
            URLSession.shared.dataTask(with: request) { _, _, error in
                // On failure the confirm page says nothing was received.
                if let error { NSLog("ckkaraoke: handing the received favorites to the server: %@", "\(error)") }
                DispatchQueue.main.async {
                    web.loadPage(URL(string: "\(base)/favorites/backup/incoming")!)
                }
            }.resume()
        }
        return true
    }

    // MARK: - Sending

    /// Sends what a ckpeer: link asks for. `failed` runs on the main queue
    /// with the other app's name when it could not be opened.
    static func send(_ link: URL, port: Int, failed: @escaping (String) -> Void) {
        guard let scheme else { return }
        let part = link.absoluteString.dropFirst("ckpeer:".count)
        let kind = part.split(separator: "?", maxSplits: 1).first.map(String.init) ?? ""

        switch kind {
        case "folder":
            let query = URLComponents(string: "x:?" + (part.split(separator: "?", maxSplits: 1).last.map(String.init) ?? ""))
            guard let code = query?.queryItems?.first(where: { $0.name == "code" })?.value else { return }
            var c = URLComponents()
            c.scheme = scheme
            c.host = "favorites"
            c.queryItems = [URLQueryItem(name: "kind", value: "folder"), URLQueryItem(name: "code", value: code)]
            open(c.url!, failed: failed)
        case "backup":
            let url = URL(string: "http://127.0.0.1:\(port)/favorites/backup.xml")!
            URLSession.shared.dataTask(with: url) { data, _, error in
                DispatchQueue.main.async {
                    guard let data, error == nil, let xml = String(data: data, encoding: .utf8) else {
                        NSLog("ckkaraoke: reading the favorites to send: %@", "\(String(describing: error))")
                        failed(name)
                        return
                    }
                    UIPasteboard(name: pasteboard, create: true)?.string = xml
                    open(URL(string: "\(scheme)://favorites?kind=backup")!, failed: failed)
                }
            }.resume()
        default:
            return
        }
    }

    private static func open(_ url: URL, failed: @escaping (String) -> Void) {
        UIApplication.shared.open(url) { opened in
            if !opened { failed(name) }
        }
    }
}

/// What Peer.deliver needs from the view controller: a way to load a page.
protocol PageLoader: AnyObject {
    func loadPage(_ url: URL)
}
