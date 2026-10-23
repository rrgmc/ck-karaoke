import UIKit
import WebKit

/// The whole UI: a startup screen, then a web view onto the server running in
/// this process, or a failure screen that doubles as the only settings there are.
///
/// Built in code rather than a storyboard, matching the Android app, which has no
/// layout XML either.
final class RootViewController: UIViewController {
    // The web UI's own palette, so handing over to it is not a visible change.
    private static let ink = UIColor(red: 0x21 / 255, green: 0x25 / 255, blue: 0x29 / 255, alpha: 1)
    private static let muted = UIColor(red: 0x6C / 255, green: 0x75 / 255, blue: 0x7D / 255, alpha: 1)
    private static let accent = UIColor(red: 0x0D / 255, green: 0x6E / 255, blue: 0xFD / 255, alpha: 1)

    private var webView: WKWebView?
    /// The startup screen's caption, kept so download progress can replace it.
    private var startingLabel: UILabel?
    /// Where a favorites backup is being written, until it is handed to the
    /// share sheet. WKDownload does not report the destination back, so the one
    /// chosen in decideDestinationUsing has to be kept.
    private var downloaded: URL?

    // The status bar sits over a white page, so its text has to be dark. The
    // startup and failure screens are painted white for the same reason: a dark
    // splash handing over to a white page reads as a flash.
    override var preferredStatusBarStyle: UIStatusBarStyle { .darkContent }

    override func viewDidLoad() {
        super.viewDidLoad()
        view.backgroundColor = .white
        showStarting()
        Server.start()
        waitForServer()
    }

    private func waitForServer() {
        Server.waitUntilReady(
            progress: { [weak self] percent in self?.showProgress(percent) },
            ready: { [weak self] url in self?.showWeb(url) },
            failed: { [weak self] reason in self?.showFailure(reason) })
    }

    /// Says what the wait is for. Downloading the catalogue takes upwards of
    /// twenty seconds on a first run, and a spinner alone gives no way to tell
    /// that from a hang — which is exactly what it looked like.
    private func showProgress(_ percent: Int?) {
        guard let percent else {
            startingLabel?.text = "Starting the karaoke server…"
            return
        }
        startingLabel?.text = "Downloading the song list… \(percent)%"
    }

    // MARK: - Startup

    private func showStarting() {
        // Cleared first: Try again reaches here from the failure screen, and
        // without this the startup screen is laid on top of it.
        clear()

        let spinner = UIActivityIndicatorView(style: .medium)
        spinner.color = Self.muted
        spinner.startAnimating()

        let label = Self.label("Starting the karaoke server…", color: Self.muted, size: 15)
        startingLabel = label

        let stack = UIStackView(arrangedSubviews: [spinner, label])
        stack.axis = .vertical
        stack.alignment = .center
        stack.spacing = 16
        centre(stack)
    }

    // MARK: - The web UI

    private func showWeb(_ url: URL) {
        clear()

        let config = WKWebViewConfiguration()
        // The page is htmx plus an event stream, so both of these are load-bearing.
        config.defaultWebpagePreferences.allowsContentJavaScript = true

        // The sync page reads a QR code from the camera. Without inline playback
        // the preview is taken fullscreen the instant it starts, hiding the page
        // it belongs to; and the play() call happens after getUserMedia has
        // resolved, by which time WebKit no longer treats it as user-initiated,
        // so it has to be allowed to start by itself. The only page this view
        // ever loads is the one served from inside this app, on loopback.
        config.allowsInlineMediaPlayback = true
        config.mediaTypesRequiringUserActionForPlayback = []

        let web = WKWebView(frame: .zero, configuration: config)
        web.navigationDelegate = self
        // Set for one reason: answering the page's request for the camera itself,
        // because WebKit's own prompt is asked again every time. See below.
        web.uiDelegate = self
        web.allowsBackForwardNavigationGestures = true
        web.backgroundColor = .white
        web.isOpaque = false
        // WKWebView would otherwise inset the content for the safe area itself and
        // then the page's own padding would be added on top of it.
        web.scrollView.contentInsetAdjustmentBehavior = .never
        web.translatesAutoresizingMaskIntoConstraints = false
        view.addSubview(web)

        // Top and sides inside the safe area, bottom deliberately outside it.
        //
        // This is the same split the Android app arrived at, for the same reason:
        // the page must not run under the status bar, but the tab bar has to reach
        // the physical bottom edge with its own padding for the home indicator.
        // Because the web view overlaps that region, WKWebView reports a real
        // safe-area inset to CSS and app.css's env(safe-area-inset-bottom) does the
        // rest — so unlike Android, nothing has to be injected into the page.
        NSLayoutConstraint.activate([
            web.topAnchor.constraint(equalTo: view.safeAreaLayoutGuide.topAnchor),
            web.leadingAnchor.constraint(equalTo: view.safeAreaLayoutGuide.leadingAnchor),
            web.trailingAnchor.constraint(equalTo: view.safeAreaLayoutGuide.trailingAnchor),
            web.bottomAnchor.constraint(equalTo: view.bottomAnchor),
        ])

        webView = web
        web.load(URLRequest(url: url))
    }

    /// Puts the page back in working order after a suspension.
    ///
    /// Called when the grace period ran out and the session was given up, rather
    /// than on every trip to the background. What actually breaks is the event
    /// stream: it does not survive being frozen, and the server goes on writing to
    /// a subscriber it still believes in while the page applies none of it and —
    /// because nothing errors — never reconnects either. That is how the offline
    /// banner came to outlive the reconnection it was reporting.
    ///
    /// So the stream is rebuilt and the document is left alone. Reloading was the
    /// first answer and was too blunt: the song list is paged with a Load more
    /// button that appends into the DOM, and the sort, known-only and ⋯ toggles set
    /// hx-push-url="false", so none of that is in the URL and a reload cannot bring
    /// it back. It returned you to the first fifty rows of an unfiltered list. The
    /// stream feeds no part of that list — only the toasts, banner, connection dot,
    /// queue count, queue and player, each a small targeted swap — so the document
    /// was being rebuilt to refresh things that were never the expensive part.
    ///
    /// The reload survives as the fallback, for a page that has no stream to rebuild
    /// — one loaded before resume.js existed, or a failure screen. It still reloads
    /// the page that was showing rather than the site root: the listener is
    /// untouched by a suspension, since Suspend gives up the device session and not
    /// the HTTP port, so a URL from before it still points at the same server.
    func resumeWeb() {
        guard let webView else { return }
        webView.evaluateJavaScript("window.ckResume ? window.ckResume() : 0") { [weak self] result, _ in
            if let rebuilt = result as? Int, rebuilt > 0 {
                NSLog("ckkaraoke: rebuilt %ld event stream(s) after a suspension", rebuilt)
                return
            }
            self?.reloadAfterSuspension()
        }
    }

    /// The fallback when there was no stream to rebuild. Kept separate so the
    /// logging says which of the two happened — they fail in different ways and
    /// the console is the only place that difference is visible.
    private func reloadAfterSuspension() {
        guard let webView, let root = Server.url else { return }
        let target = webView.url.flatMap { isOurs($0) ? $0 : nil } ?? root
        NSLog("ckkaraoke: no stream to rebuild; reloading %@ after a suspension", target.absoluteString)
        webView.load(URLRequest(url: target))
    }

    // MARK: - Failure, and the only settings screen there is

    private let addressField = UITextField()
    private let nameField = UITextField()

    /// Shown when the server gives up. The address field is here rather than
    /// behind a settings button because this is the only moment it matters: the
    /// sweep normally finds the unit, and when it does not, this is the screen you
    /// are already looking at.
    private func showFailure(_ reason: String) {
        clear()

        let title = Self.label("Cannot reach the karaoke device", color: Self.ink, size: 17, weight: .semibold)
        let detail = Self.label(reason, color: Self.muted, size: 13)

        Self.configure(addressField, placeholder: "Device address, e.g. 192.168.1.146")
        addressField.text = Settings.deviceAddress
        addressField.keyboardType = .numbersAndPunctuation

        Self.configure(nameField, placeholder: "Name shown beside queued songs")
        nameField.text = Settings.name

        let retry = UIButton(type: .system)
        retry.setTitle("Try again", for: .normal)
        retry.setTitleColor(.white, for: .normal)
        retry.backgroundColor = Self.accent
        retry.titleLabel?.font = .systemFont(ofSize: 16, weight: .semibold)
        retry.layer.cornerRadius = 10
        retry.heightAnchor.constraint(equalToConstant: 48).isActive = true
        retry.addTarget(self, action: #selector(retryTapped), for: .touchUpInside)

        let hint = Self.label("Leave the address empty to search the network again.",
                              color: Self.muted, size: 12)

        let stack = UIStackView(arrangedSubviews: [title, detail, addressField, nameField, retry, hint])
        stack.axis = .vertical
        stack.spacing = 14
        centre(stack)

        view.addGestureRecognizer(UITapGestureRecognizer(target: view, action: #selector(UIView.endEditing(_:))))
    }

    /// Whether a failed page load has already been answered with a restart. Reset
    /// every time a page does load, so each outage gets one attempt and a server
    /// that is truly broken still reaches the failure screen.
    private var didRetryLoad = false

    /// Handles a page that would not load.
    ///
    /// The server is in this process, so a page that will not load means the server
    /// has stopped serving — not that the address is wrong. Restarting it is both
    /// the right response and one the user should not have to ask for: this is what
    /// the app looked like after a long spell in the background, and the only thing
    /// on offer was a Try again that could not work.
    private func showLoadFailure(_ error: Error) {
        let target = Server.url?.absoluteString ?? "the local server"
        NSLog("ckkaraoke: loading %@ failed: %@ (port=%d, error=%@)",
              target, error.localizedDescription, Server.port ?? 0,
              Server.failure ?? "none")

        if !didRetryLoad {
            didRetryLoad = true
            NSLog("ckkaraoke: restarting the server after a failed load")
            showStarting()
            restartServer()
            return
        }
        showFailure("Could not load \(target)\n\(error.localizedDescription)")
    }

    @objc private func retryTapped() {
        view.endEditing(true)
        Settings.deviceAddress = addressField.text
        Settings.name = nameField.text

        showStarting()
        restartServer()
    }

    /// Stops the server and starts it again, then waits for whatever comes back.
    ///
    /// Stopping first is what makes this a restart rather than a no-op: CKStart
    /// refuses while a run is still going, and a run that has stopped serving
    /// without returning still counts as going. Skipping the stop left Try again
    /// doing nothing, and the wait then handed straight back the port of the server
    /// that had already failed — so it reloaded the same dead address and returned
    /// to this screen, for as long as anyone kept pressing it.
    ///
    /// Stopping blocks for up to six seconds, hence the hop off the main thread.
    private func restartServer() {
        DispatchQueue.global(qos: .userInitiated).async { [weak self] in
            Server.stop()
            let started = Server.start()
            DispatchQueue.main.async {
                guard let self else { return }
                // Checked, because the one way this fails is a previous run that has
                // not finished winding down, and it reports no reason when it does.
                // Waiting on that would be waiting for a port that is never coming.
                guard started else {
                    NSLog("ckkaraoke: the server refused to restart")
                    self.showFailure("The server did not start again.\nClose the app and open it once more.")
                    return
                }
                self.waitForServer()
            }
        }
    }

    // MARK: - Layout helpers

    private func clear() {
        webView = nil
        startingLabel = nil
        view.subviews.forEach { $0.removeFromSuperview() }
        view.gestureRecognizers?.forEach(view.removeGestureRecognizer)
    }

    private func centre(_ stack: UIStackView) {
        stack.translatesAutoresizingMaskIntoConstraints = false
        view.addSubview(stack)
        NSLayoutConstraint.activate([
            stack.centerYAnchor.constraint(equalTo: view.safeAreaLayoutGuide.centerYAnchor),
            stack.leadingAnchor.constraint(equalTo: view.safeAreaLayoutGuide.leadingAnchor, constant: 28),
            stack.trailingAnchor.constraint(equalTo: view.safeAreaLayoutGuide.trailingAnchor, constant: -28),
        ])
    }

    private static func label(_ text: String, color: UIColor, size: CGFloat,
                              weight: UIFont.Weight = .regular) -> UILabel {
        let l = UILabel()
        l.text = text
        l.textColor = color
        l.font = .systemFont(ofSize: size, weight: weight)
        l.numberOfLines = 0
        l.textAlignment = .center
        return l
    }

    private static func configure(_ field: UITextField, placeholder: String) {
        field.placeholder = placeholder
        field.borderStyle = .roundedRect
        field.font = .systemFont(ofSize: 15)
        field.autocorrectionType = .no
        field.autocapitalizationType = .none
        field.clearButtonMode = .whileEditing
        field.heightAnchor.constraint(equalToConstant: 44).isActive = true
    }
}

extension RootViewController: WKNavigationDelegate {
    /// Keeps the app on its own server and hands everything else to iOS.
    ///
    /// Decided by host, not by naming youtube.com. This view only ever shows the
    /// server running inside this app, so any other host is by definition a link
    /// out — the YouTube search on each song row today, whatever gets linked
    /// tomorrow. It is the rule the Android wrapper applies, and the one
    /// requestMediaCapturePermissionFor below already uses to decide the camera.
    ///
    /// Only navigations a person caused are handed out: .linkActivated and
    /// .formSubmitted. .other emphatically is not, because .other is what the
    /// initial load, every server redirect and everything htmx does arrive as, and
    /// treating one of those as external would bounce the user into Safari for a
    /// page meant to render here. Anything else on a foreign host is cancelled
    /// rather than allowed: allowing it would replace the whole UI with a page
    /// there is no way back from, and were that page to fail,
    /// didFailProvisionalNavigation would read it as the local server dying and
    /// restart it.
    func webView(_ webView: WKWebView, decidePolicyFor navigationAction: WKNavigationAction,
                 decisionHandler: @escaping (WKNavigationActionPolicy) -> Void) {
        guard let url = navigationAction.request.url, !isOurs(url) else {
            // A target="_blank" to our own server would otherwise be dropped on
            // the floor: WebKit asks createWebViewWith for somewhere to put it,
            // there is nowhere, and nothing happens at all. Nothing in the page
            // does this today; loading it in place is the sane answer if it ever
            // does.
            if navigationAction.targetFrame == nil, let ours = navigationAction.request.url {
                decisionHandler(.cancel)
                webView.load(URLRequest(url: ours))
                return
            }
            decisionHandler(.allow)
            return
        }

        switch navigationAction.navigationType {
        case .linkActivated, .formSubmitted:
            decisionHandler(.cancel)
            openOutside(url)
        default:
            NSLog("ckkaraoke: refusing a type-%ld navigation to %@",
                  navigationAction.navigationType.rawValue, url.absoluteString)
            decisionHandler(.cancel)
        }
    }

    /// Our own server. The port moves between launches; the host does not. A URL
    /// with no host — about:blank, a data: URL — is not a link out.
    private func isOurs(_ url: URL) -> Bool {
        guard let host = url.host else { return true }
        return host == "127.0.0.1" || host == "localhost"
    }

    /// Hands a link to the app that owns it, and to the browser only if none does.
    ///
    /// Asked as two questions rather than one. A plain `open` leaves the whole
    /// choice to the system, and that choice is not stable: the same YouTube search
    /// opened the app, opened Chrome, and once appeared to do both, in the period
    /// after YouTube was reinstalled and its association was still being fetched.
    /// `universalLinksOnly` asks only the narrow question — is there an app claiming
    /// this URL — and reports false rather than quietly opening a browser instead.
    /// The fallback then happens because this code chose it, which is the part that
    /// was previously being inherited and guessed at.
    ///
    /// No host is named here either. The rule is "the owning app if there is one",
    /// which is the same general rule decidePolicyFor uses to decide the link is
    /// leaving at all; a site that registers no app simply always takes the second
    /// branch. canOpenURL still is not used — it answers against
    /// LSApplicationQueriesSchemes, which this app does not declare, and neither
    /// call here needs it.
    ///
    /// The completion handler runs on the main queue and captures nothing but the
    /// URL, so there is no cycle to break and no thread to hop.
    private func openOutside(_ url: URL) {
        UIApplication.shared.open(url, options: [.universalLinksOnly: true]) { opened in
            guard !opened else { return }
            NSLog("ckkaraoke: no app claims %@; handing it to the browser", url.absoluteString)
            UIApplication.shared.open(url)
        }
    }

    /// Turns the favorites export into a download rather than a page.
    ///
    /// Without this a WKWebView does nothing useful with an attachment: there is
    /// no built-in download UI on iOS, so the response is simply not rendered and
    /// the user is left looking at the page they were already on, with no sign
    /// that anything happened.
    ///
    /// Decided by the header rather than by naming the URL, which is the same
    /// rule the rest of this file follows: the server says what is a file, and
    /// anything it serves as an attachment from now on is handled without this
    /// needing to know about it.
    func webView(_ webView: WKWebView, decidePolicyFor navigationResponse: WKNavigationResponse,
                 decisionHandler: @escaping (WKNavigationResponsePolicy) -> Void) {
        guard let http = navigationResponse.response as? HTTPURLResponse,
              let disposition = http.value(forHTTPHeaderField: "Content-Disposition"),
              disposition.lowercased().hasPrefix("attachment") else {
            decisionHandler(.allow)
            return
        }
        decisionHandler(.download)
    }

    func webView(_ webView: WKWebView, navigationResponse: WKNavigationResponse,
                 didBecome download: WKDownload) {
        download.delegate = self
    }

    // A failed load must not be a blank white screen. The server is in this same
    // process, so a failure here means the page could not be fetched from
    // loopback — a wrong port, or App Transport Security refusing plain HTTP —
    // and neither is guessable from an empty page.
    //
    // With one exception, and it is a load-bearing one: turning a navigation
    // into a download cancels that navigation, and WebKit may report the
    // cancellation here as error 102, frame load interrupted by a policy change.
    // Read as a load failure it restarts the Go server — so saving a backup
    // would tear down the very server that had just served it, and the first
    // symptom would be the startup screen appearing for no reason. Whether
    // WebKit raises it for a .download decision or only for an outright .cancel
    // has varied between versions, so it is filtered rather than relied upon
    // either way; the guard costs nothing and no navigation this app makes
    // should ever be treated as the server dying.
    func webView(_ webView: WKWebView, didFailProvisionalNavigation navigation: WKNavigation!,
                 withError error: Error) {
        guard !isPolicyChange(error) else { return }
        showLoadFailure(error)
    }

    func webView(_ webView: WKWebView, didFail navigation: WKNavigation!, withError error: Error) {
        guard !isPolicyChange(error) else { return }
        showLoadFailure(error)
    }

    /// Whether an error is WebKit saying "that navigation became something else".
    ///
    /// Matched by domain and code rather than by naming a constant, because
    /// WebKitErrorFrameLoadInterruptedByPolicyChange is not exposed to Swift.
    private func isPolicyChange(_ error: Error) -> Bool {
        let e = error as NSError
        return e.domain == "WebKitErrorDomain" && e.code == 102
    }

    /// A page that loaded earns back the one restart a failure is allowed.
    ///
    /// Without this the allowance is spent once per launch, and the second time the
    /// server goes away — another long suspension, say — the app gives up and shows
    /// the failure screen instead of doing the thing that worked the first time.
    func webView(_ webView: WKWebView, didFinish navigation: WKNavigation!) {
        didRetryLoad = false
    }

    func webViewWebContentProcessDidTerminate(_ webView: WKWebView) {
        showLoadFailure(NSError(domain: "CKKaraoke", code: -1, userInfo: [
            NSLocalizedDescriptionKey: "The web view stopped unexpectedly.",
        ]))
    }
}

extension RootViewController: WKUIDelegate {
    /// Answers the page's request for the camera, instead of letting WebKit ask.
    ///
    /// WebKit's own prompt is not remembered — it is asked on every getUserMedia
    /// call, so pressing Scan a second time asked a second time, with no way to
    /// say yes once. Deciding here replaces it, and the permission that *does*
    /// persist is then the only one the user sees: iOS's camera prompt, asked once
    /// against NSCameraUsageDescription and revocable in Settings afterwards.
    ///
    /// This is the arrangement the Android wrapper already has, arrived at for the
    /// same reason — its WebChromeClient grants RESOURCE_VIDEO_CAPTURE itself once
    /// the system CAMERA prompt has been answered.
    ///
    /// Granting rests on what this web view can load, which is only the server
    /// running inside this app on loopback; the host is checked rather than assumed
    /// so that stays true by construction. Microphone requests are refused because
    /// nothing in the page makes one: scan.js asks for `audio: false`, and a
    /// request for it would mean the page is not the one this app serves.
    func webView(_ webView: WKWebView,
                 requestMediaCapturePermissionFor origin: WKSecurityOrigin,
                 initiatedByFrame frame: WKFrameInfo,
                 type: WKMediaCaptureType,
                 decisionHandler: @escaping (WKPermissionDecision) -> Void) {
        guard type == .camera, origin.host == "127.0.0.1" else {
            NSLog("ckkaraoke: refusing a media request for %@ from %@",
                  String(describing: type), origin.host)
            decisionHandler(.deny)
            return
        }
        decisionHandler(.grant)
    }
}

/// Saving a favorites backup out of the app.
///
/// The file is written to a temporary directory and then offered to the share
/// sheet, which is what reaches Save to Files, Dropbox and Drive — the same three
/// destinations the Android app's document picker offers, arrived at by the
/// mechanism iOS actually has. Nothing is kept: the copy the user chose to keep
/// is the one the sheet handed to another app, and this one is temporary storage
/// the system reclaims.
///
/// Deliberately *not* the app's Documents directory. That is where a
/// UIFileSharingEnabled app puts files for the Files app to find, and this app
/// keeps nothing there on purpose — see ios/README.md — because Documents is
/// iCloud-backed and the databases have no business being copied off the device
/// by themselves.
extension RootViewController: WKDownloadDelegate {
    func download(_ download: WKDownload,
                  decideDestinationUsing response: URLResponse,
                  suggestedFilename: String,
                  completionHandler: @escaping (URL?) -> Void) {
        // A fresh directory per download, because WebKit refuses a destination
        // that already exists and saving a second backup on the same day would
        // otherwise land on the first one's name.
        let directory = FileManager.default.temporaryDirectory
            .appendingPathComponent("backup-\(UUID().uuidString)", isDirectory: true)
        do {
            try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
        } catch {
            NSLog("ckkaraoke: no room to write a backup: %@", error.localizedDescription)
            completionHandler(nil)
            return
        }

        let name = suggestedFilename.isEmpty ? "ck-karaoke-favorites.xml" : suggestedFilename
        let file = directory.appendingPathComponent(name)
        downloaded = file
        completionHandler(file)
    }

    func downloadDidFinish(_ download: WKDownload) {
        guard let file = downloaded else { return }
        downloaded = nil
        share(file)
    }

    func download(_ download: WKDownload, didFailWithError error: Error, resumeData: Data?) {
        NSLog("ckkaraoke: a download failed: %@", error.localizedDescription)
        downloaded = nil
        // Its own alert, and emphatically not showLoadFailure: a backup that
        // could not be written is a thing that did not happen, not the server
        // going away, and restarting it would throw the page away for nothing.
        let alert = UIAlertController(title: "The backup could not be saved",
                                      message: error.localizedDescription,
                                      preferredStyle: .alert)
        alert.addAction(UIAlertAction(title: "OK", style: .default))
        present(alert, animated: true)
    }

    /// Offers the file to whatever the user wants to do with it.
    private func share(_ file: URL) {
        let sheet = UIActivityViewController(activityItems: [file], applicationActivities: nil)

        // Required, not tidiness: on iPad a share sheet with no anchor has
        // nowhere to point and raises rather than presenting, and the iPad is
        // the device this app is used on. The page is what it came from, so the
        // page's centre is where it points.
        if let popover = sheet.popoverPresentationController {
            popover.sourceView = view
            popover.sourceRect = CGRect(x: view.bounds.midX, y: view.bounds.midY,
                                        width: 0, height: 0)
            popover.permittedArrowDirections = []
        }

        // Presented rather than added to the view hierarchy, which also means
        // clear() — it removes every subview — cannot take it away mid-share.
        present(sheet, animated: true)
    }
}
