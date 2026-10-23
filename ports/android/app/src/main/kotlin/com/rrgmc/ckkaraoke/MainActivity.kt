package com.rrgmc.ckkaraoke

import android.Manifest
import android.annotation.TargetApi
import android.app.Activity
import android.content.ActivityNotFoundException
import android.content.Context
import android.content.Intent
import android.content.pm.PackageManager
import android.net.Uri
import android.os.Bundle
import android.os.Build
import android.os.Handler
import android.os.Looper
import android.util.Log
import android.view.Gravity
import android.view.View
import android.view.ViewGroup
import android.view.WindowInsets
import android.view.WindowInsetsController
import android.webkit.PermissionRequest
import android.webkit.ValueCallback
import android.webkit.WebChromeClient
import android.webkit.WebResourceRequest
import android.webkit.WebView
import android.webkit.WebViewClient
import android.widget.Button
import android.widget.FrameLayout
import android.widget.LinearLayout
import android.widget.TextView
import android.widget.Toast
import android.window.OnBackInvokedCallback
import android.window.OnBackInvokedDispatcher
import java.io.IOException
import java.net.HttpURLConnection
import java.net.URL

/**
 * The whole UI: a WebView showing the server that is running inside this app.
 *
 * There is no native interface to build, because the interface already exists —
 * the same pages the desktop server serves, over loopback rather than the LAN.
 */
class MainActivity : Activity() {

    private val main = Handler(Looper.getMainLooper())
    private var waiting: Thread? = null

    /** A page's camera request, held while Android's own dialog is answered. */
    private var pendingCamera: PermissionRequest? = null

    /** The URL of a download, held while the user picks where to put it. */
    private var pendingDownload: String? = null

    /** A page's file input, held while the user picks a file for it. */
    private var pendingChooser: ValueCallback<Array<Uri>>? = null

    private fun hasCamera(): Boolean =
        checkSelfPermission(Manifest.permission.CAMERA) == PackageManager.PERMISSION_GRANTED

    /**
     * Answers the page's camera request once the system dialog has been.
     *
     * Denying explicitly rather than dropping the request matters: a request
     * left unanswered leaves getUserMedia pending forever, so the scanner would
     * sit on "Starting the camera…" instead of saying it was refused.
     */
    override fun onRequestPermissionsResult(
        requestCode: Int, permissions: Array<out String>, results: IntArray,
    ) {
        super.onRequestPermissionsResult(requestCode, permissions, results)
        if (requestCode != REQ_CAMERA) return

        val request = pendingCamera ?: return
        pendingCamera = null
        if (results.isNotEmpty() && results[0] == PackageManager.PERMISSION_GRANTED) {
            request.grant(arrayOf(PermissionRequest.RESOURCE_VIDEO_CAPTURE))
        } else {
            request.deny()
        }
    }

    /**
     * Answers the two pickers a favorites backup needs.
     *
     * The first onActivityResult in this app, and both halves are here because
     * both are answers to something the page is waiting on. There is no androidx
     * dependency — none at all, deliberately — so registerForActivityResult does
     * not exist and this is the mechanism there is. The framework's own
     * onActivityResult is not deprecated; only androidx's replacement of it is.
     */
    override fun onActivityResult(requestCode: Int, resultCode: Int, data: Intent?) {
        super.onActivityResult(requestCode, resultCode, data)
        when (requestCode) {
            REQ_SAVE -> {
                val url = pendingDownload
                pendingDownload = null
                val target = data?.data
                // A cancelled save is not a failure and says nothing; the page
                // is still there and the button still works.
                if (resultCode != RESULT_OK || url == null || target == null) return
                saveDownload(url, target)
            }

            REQ_OPEN -> {
                val callback = pendingChooser
                pendingChooser = null
                // Answered on every path, cancellation included — see
                // onShowFileChooser. onReceiveValue(null) is how a WebView is
                // told the user chose nothing, and it is what leaves the input
                // able to ask again.
                callback?.onReceiveValue(
                    if (resultCode == RESULT_OK) data?.data?.let { arrayOf(it) } else null
                )
            }
        }
    }

    /**
     * Copies the server's response into wherever the picker pointed.
     *
     * On a thread, because it is a network read and a file write, and the URL is
     * re-fetched rather than the bytes being held from the download listener —
     * the listener only ever gets a URL, and this server is on loopback with
     * nothing to authenticate against, so asking for it again is a plain GET.
     * A backup is measured in tens of kilobytes, so nothing here is progressive.
     */
    private fun saveDownload(url: String, target: Uri) {
        Thread({
            val error = try {
                val connection = URL(url).openConnection() as HttpURLConnection
                try {
                    connection.inputStream.use { source ->
                        val sink = contentResolver.openOutputStream(target)
                            ?: throw IOException("nothing would open $target for writing")
                        sink.use { source.copyTo(it) }
                    }
                } finally {
                    connection.disconnect()
                }
                null
            } catch (e: Exception) {
                Log.w(Server.TAG, "saving $url", e)
                e.message ?: "it could not be written"
            }

            main.post {
                if (error == null) toast("Backup saved.") else toast("Could not save it: $error")
            }
        }, "ckkaraoke-save").apply { isDaemon = true }.start()
    }

    private fun toast(text: String) =
        Toast.makeText(this, text, Toast.LENGTH_LONG).show()

    /**
     * The name to offer in the save dialog, taken from the server's own header.
     *
     * Parsed rather than guessed so the date in the filename is the server's.
     * If the header is ever missing or shaped differently, a fixed name is a far
     * better outcome than an empty one — the picker shows the field blank and
     * refuses to save until something is typed.
     */
    private fun downloadName(disposition: String?): String {
        val match = FILENAME.find(disposition ?: "")
        val name = match?.groupValues?.getOrNull(1)?.trim('"', ' ')
        return if (name.isNullOrEmpty()) "ckkaraoke-favorites.xml" else name
    }

    /**
     * Starting the server is onStart's job, not this one's.
     *
     * onStart always follows onCreate, and it has to be able to start the
     * server anyway — the grace period may have stopped it while the Activity
     * lived on. Doing it in both places meant a first launch started it twice
     * and painted the splash twice, because the service sets itself as the
     * running instance asynchronously and is not visible yet by the time
     * onStart looks.
     */
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        showStarting()
    }

    /**
     * Waits for the server to bind, for as long as it is alive.
     *
     * Not a short fixed deadline: the first run downloads the whole song
     * catalogue from the device before it can listen, which measured 20.2s on a
     * Galaxy S23 — and a 20s timeout turned a working server into "did not start
     * in time". What actually means failure is the process exiting, so that is
     * what is waited on. The cap is only a backstop against waiting forever.
     */
    private fun waitForServer() {
        if (waiting?.isAlive == true) return
        waiting = Thread({
            val deadline = System.currentTimeMillis() + WAIT_LIMIT_MS
            while (System.currentTimeMillis() < deadline) {
                val port = Server.port
                if (port != 0) {
                    main.post { showWeb(port) }
                    return@Thread
                }
                if (Server.failure != null) break
                try {
                    Thread.sleep(100)
                } catch (e: InterruptedException) {
                    return@Thread
                }
            }
            main.post {
                showFailure(Server.failure ?: "the server is taking longer than expected to start")
            }
        }, "ckkaraoke-wait").apply { isDaemon = true }
        waiting?.start()
    }

    /** The server may have finished binding while a failure screen was up. */
    override fun onResume() {
        super.onResume()
        // `web` is non-null exactly while a page is showing, which is what this
        // wants to know. currentFocus was standing in for it, and stopped being
        // able to the moment leaving the app became an ordinary thing to do:
        // coming back from YouTube with the focus dropped rebuilt the WebView
        // and put the user back at the top of the catalogue.
        if (web != null) return
        if (Server.port != 0) waitForServer()
    }

    /**
     * Back on screen: call off the countdown, and wake everything it stopped.
     *
     * onStart rather than onResume, and onStop rather than onPause, because the
     * two file pickers and the camera prompt all pause this Activity without it
     * ceasing to be the thing the user is doing. Starting a ten-minute clock
     * towards killing the server because someone opened the document picker to
     * restore a backup would be absurd — and pausing the WebView underneath a
     * picker it is waiting on is worse than absurd.
     *
     * If the grace period did run out, the service is gone and so is the Go
     * process. Starting it here is legal where it would not have been in onStop:
     * an app coming to the foreground may start a foreground service. The port
     * will be a new one — the server binds 127.0.0.1:0 — so this is a genuine
     * restart with a fresh page, not a resumption, and showStarting covers the
     * few seconds it takes.
     */
    override fun onStart() {
        super.onStart()
        ServerService.appForegrounded()

        if (!ServerService.isRunning) {
            // showStarting swaps the content view, which is what detaches the
            // old WebView; discarding it only after that is what makes the
            // destroy() legal.
            val stale = web
            showStarting()
            discard(stale)
            ServerService.start(this, savedDeviceAddress())
            waitForServer()
            return
        }

        web?.onResume()
        web?.resumeTimers()
    }

    /**
     * Tears a WebView down for good.
     *
     * destroy() is what releases the renderer and closes the event stream still
     * open on it; dropping the reference alone leaves both. It may not be called
     * while the view is still in a hierarchy, hence the removal first.
     */
    private fun discard(w: WebView?) {
        if (w == null) return
        (w.parent as? ViewGroup)?.removeView(w)
        w.destroy()
    }

    /**
     * Off screen: start the countdown, and stop the page working for nobody.
     *
     * Without the pause the WebView goes on holding its event stream open and
     * swapping DOM on every status push — about forty a minute — behind a screen
     * that is off. pauseTimers is process-wide rather than per-view, which is
     * exactly right here and would need care in an app with a second WebView.
     */
    override fun onStop() {
        web?.onPause()
        web?.pauseTimers()
        ServerService.appBackgrounded()
        super.onStop()
    }

    private fun showWeb(port: Int) {
        // Never two of them. This is reached from a posted callback, so a second
        // one can in principle arrive while the first is still on screen — and
        // the one being replaced holds a renderer and an open event stream until
        // it is told otherwise.
        discard(this.web)
        bindWeb(null)

        val web = WebView(this).apply {
            layoutParams = ViewGroup.LayoutParams(
                ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.MATCH_PARENT
            )
            settings.javaScriptEnabled = true      // htmx and the event stream
            settings.domStorageEnabled = true
            // Our own pages load here; anything else goes to whatever app the
            // system thinks owns it.
            webViewClient = object : WebViewClient() {
                override fun shouldOverrideUrlLoading(
                    view: WebView?, request: WebResourceRequest?,
                ): Boolean {
                    val url = request?.url ?: return false
                    if (staysInside(url)) return false
                    return openOutside(url)
                }

                // Every page is a fresh document, so the measurement has to be
                // re-applied rather than set once.
                override fun onPageFinished(view: WebView?, url: String?) {
                    applySafeBottom()
                }
            }

            // The sync page reads a QR code from the camera. The stream starts
            // from a tap, but the play() call happens after getUserMedia has
            // resolved, by which time the WebView no longer counts it as a
            // gesture — so autoplay has to be allowed or the video never runs.
            // The only page this WebView ever loads is our own, on loopback.
            settings.mediaPlaybackRequiresUserGesture = false

            // A WebView grants nothing to the page by itself: even with the
            // app holding CAMERA, getUserMedia fails until this says yes.
            webChromeClient = object : WebChromeClient() {
                override fun onPermissionRequest(request: PermissionRequest) {
                    if (!request.resources.contains(PermissionRequest.RESOURCE_VIDEO_CAPTURE)) {
                        request.deny()
                        return
                    }
                    if (hasCamera()) {
                        request.grant(arrayOf(PermissionRequest.RESOURCE_VIDEO_CAPTURE))
                        return
                    }
                    // Two permissions, asked in order: Android's, then the
                    // page's. The request is held until the user has answered
                    // the system dialog, because granting it before that would
                    // hand the page a camera the app itself may not open.
                    pendingCamera = request
                    requestPermissions(arrayOf(Manifest.permission.CAMERA), REQ_CAMERA)
                }

                /**
                 * Opens a document picker for the restore page's file input.
                 *
                 * A WebView does nothing at all for <input type="file"> until
                 * this is implemented — the control looks live and is not. The
                 * base class returns false, which is how it stayed unnoticed.
                 */
                override fun onShowFileChooser(
                    view: WebView?,
                    callback: ValueCallback<Array<Uri>>?,
                    params: FileChooserParams?,
                ): Boolean {
                    // Any earlier request is answered before this one replaces
                    // it. Dropping one silently is the same trap the camera
                    // comment above records, reached a different way: a
                    // ValueCallback that is never called leaves the input
                    // permanently dead, so the second Restore does nothing and
                    // there is nothing on screen to say why.
                    pendingChooser?.onReceiveValue(null)
                    pendingChooser = callback

                    // */* rather than text/xml, and built here rather than by
                    // params.createIntent(), which would turn the input's own
                    // accept list into a filter. That is the thing to avoid:
                    // the file is very often coming from Drive or Dropbox, which
                    // report a MIME type of their own choosing for what they
                    // hold, and a filter is what makes a backup that is plainly
                    // there impossible to pick. The accept list is still worth
                    // having — iOS reads it — so it stays in the page.
                    val pick = Intent(Intent.ACTION_OPEN_DOCUMENT)
                        .addCategory(Intent.CATEGORY_OPENABLE)
                        .setType("*/*")
                    return try {
                        startActivityForResult(pick, REQ_OPEN)
                        true
                    } catch (e: ActivityNotFoundException) {
                        Log.w(Server.TAG, "no document picker on this device", e)
                        pendingChooser = null
                        callback?.onReceiveValue(null)
                        false
                    }
                }
            }

            /**
             * Saves what the server offered as a download.
             *
             * This fires because the favorites export is an ordinary
             * http://127.0.0.1 navigation carrying Content-Disposition:
             * attachment — staysInside returns it to the WebView, which reads
             * the header and comes here instead of rendering it. Worth knowing
             * before "improving" the export into a blob: URL, which staysInside
             * also keeps and the WebView cannot render, so nothing at all would
             * happen.
             *
             * ACTION_CREATE_DOCUMENT rather than a share sheet: this is a save,
             * and the picker lists on-device storage, Drive and Dropbox as
             * destinations, which is the whole of what was asked for. It also
             * needs no storage permission and no FileProvider — the app never
             * holds the file, it copies the response straight into the stream
             * the picker hands back.
             */
            setDownloadListener { url, _, disposition, _, _ ->
                pendingDownload = url
                val save = Intent(Intent.ACTION_CREATE_DOCUMENT)
                    .addCategory(Intent.CATEGORY_OPENABLE)
                    .setType("text/xml")
                    .putExtra(Intent.EXTRA_TITLE, downloadName(disposition))
                try {
                    startActivityForResult(save, REQ_SAVE)
                } catch (e: ActivityNotFoundException) {
                    Log.w(Server.TAG, "no document picker on this device", e)
                    pendingDownload = null
                    toast("This phone has no app that can save a file.")
                }
            }
        }

        // targetSdk 35+ makes the window edge to edge, so without this the page
        // starts underneath the clock and the battery icon, and its bottom tab
        // bar sits under the gesture handle. The page cannot fix this itself:
        // a plain WebView does not report safe-area insets to CSS, so
        // env(safe-area-inset-*) is zero and the padding has to be applied here.
        val root = FrameLayout(this).apply {
            setBackgroundColor(PAGE_BACKGROUND)
            addView(web)
        }
        // Padded at the top and sides, but NOT at the bottom: the page's tab bar
        // should run to the very edge and paint the gesture area itself, the way
        // a native bottom bar does. Padding here instead left a blank strip
        // below it. The inset is handed to the page as --safe-bottom, which the
        // stylesheet already reads, so the bar pads its own contents clear.
        root.setOnApplyWindowInsetsListener { v, insets ->
            val (left, top, right, bottom) =
                if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.R) {
                    val bars = insets.getInsets(WindowInsets.Type.systemBars())
                    Insets4(bars.left, bars.top, bars.right, bars.bottom)
                } else {
                    @Suppress("DEPRECATION")
                    Insets4(
                        insets.systemWindowInsetLeft, insets.systemWindowInsetTop,
                        insets.systemWindowInsetRight, insets.systemWindowInsetBottom,
                    )
                }
            v.setPadding(left, top, right, 0)
            bottomInsetPx = bottom
            applySafeBottom()
            insets
        }

        setContentView(root)
        useDarkStatusBarIcons()
        bindWeb(web)
        web.loadUrl("http://127.0.0.1:$port/")
    }

    /**
     * Whether a navigation belongs in this WebView.
     *
     * Decided by host, not by naming youtube.com. This view only ever shows the
     * server running inside this app, so any other host is by definition a link
     * out — the YouTube search on each song row today, whatever gets linked
     * tomorrow. Stating the general rule is honest; a special case for one site
     * would be a list to keep in step with the page.
     *
     * Anything that is not http(s) — data:, blob:, about: — stays, because it
     * has no host to judge and nothing out there could open it anyway.
     */
    private fun staysInside(url: Uri): Boolean {
        if (url.scheme != "http" && url.scheme != "https") return true
        return url.host == "127.0.0.1" || url.host == "localhost"
    }

    /**
     * Hands a link to whatever app claims it, and says whether that worked.
     *
     * No <queries> entry is needed. Android 11's package visibility filters what
     * an app may look *up* — resolveActivity, queryIntentActivities — not what
     * an implicit intent may start, and a browser is visible to everything
     * regardless. YouTube's activity carries the intent filter for its own https
     * links, which is the same mechanism the iOS side leans on, so an installed
     * YouTube takes this and a phone without one falls through to the browser.
     *
     * FLAG_ACTIVITY_NEW_TASK is deliberately not set. Started from an Activity
     * the target lands on top of this task, which is what makes the back gesture
     * come straight back to the song list; NEW_TASK would give YouTube a task of
     * its own and back would then walk YouTube's stack instead. The flag is only
     * required when starting from a non-Activity context.
     *
     * Returning false when nothing can handle it lets the WebView load the page
     * itself, so a tap that cannot leave still does something. That is safe here
     * in a way it would not be on iOS: it is a plain https page the WebView can
     * render, and backing out of it already works — the WebView has history now,
     * so handleBack finds canGoBack true.
     */
    private fun openOutside(url: Uri): Boolean =
        try {
            startActivity(Intent(Intent.ACTION_VIEW, url))
            true
        } catch (e: ActivityNotFoundException) {
            Log.w(Server.TAG, "nothing can open $url; loading it here instead", e)
            false
        }

    /**
     * The web UI is light whatever the phone's theme is, so the status bar icons
     * have to be dark or they vanish into the white header.
     */
    private fun useDarkStatusBarIcons() {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.R) {
            window.insetsController?.setSystemBarsAppearance(
                WindowInsetsController.APPEARANCE_LIGHT_STATUS_BARS or
                    WindowInsetsController.APPEARANCE_LIGHT_NAVIGATION_BARS,
                WindowInsetsController.APPEARANCE_LIGHT_STATUS_BARS or
                    WindowInsetsController.APPEARANCE_LIGHT_NAVIGATION_BARS,
            )
        }
    }

    private var web: WebView? = null
    private var bottomInsetPx = 0

    private data class Insets4(val left: Int, val top: Int, val right: Int, val bottom: Int)

    /**
     * Tells the page how much room the gesture bar needs.
     *
     * A plain WebView reports no safe areas, so env(safe-area-inset-bottom) is
     * zero inside it and the stylesheet cannot work this out for itself. The
     * value is converted to CSS pixels, which are density-independent.
     */
    private fun applySafeBottom() {
        val w = web ?: return
        val cssPx = (bottomInsetPx / resources.displayMetrics.density).toInt()
        w.evaluateJavascript(
            "document.documentElement.style.setProperty('--safe-bottom', '" + cssPx + "px')",
            null,
        )
    }

    private fun showStarting() {
        bindWeb(null)
        setContentView(
            message(
                "Starting the karaoke server…",
                "The first run downloads the song catalogue from the device, which takes " +
                    "a few seconds. Later launches are immediate.",
                null,
            )
        )
        useDarkStatusBarIcons()
    }

    private fun showFailure(reason: String) {
        bindWeb(null)
        setContentView(
            message("Could not start the karaoke server.", reason) {
                ServerService.start(this, savedDeviceAddress())
                showStarting()
                waitForServer()
            }
        )
        useDarkStatusBarIcons()
    }

    /**
     * These screens are painted light explicitly rather than inheriting the
     * device theme, which is dark on a phone in dark mode — and the app it is
     * introducing is light whatever the phone does. A dark splash handing over
     * to a white page is a flash, not a transition.
     */
    private fun message(title: String, detail: String, retry: (() -> Unit)?): View {
        val content = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            gravity = Gravity.CENTER_VERTICAL
            setPadding(64, 96, 64, 96)
            addView(TextView(this@MainActivity).apply {
                text = title
                textSize = 20f
                setTextColor(TEXT_PRIMARY)
            })
            addView(TextView(this@MainActivity).apply {
                text = detail
                textSize = 15f
                setPadding(0, 32, 0, 0)
                setTextColor(TEXT_MUTED)
            })
            if (retry != null) {
                addView(Button(this@MainActivity).apply {
                    text = "Try again"
                    setTextColor(ACCENT)
                    setOnClickListener { retry() }
                })
            }
        }

        return FrameLayout(this).apply {
            setBackgroundColor(PAGE_BACKGROUND)
            addView(content)
            setOnApplyWindowInsetsListener { v, insets ->
                if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.R) {
                    val bars = insets.getInsets(WindowInsets.Type.systemBars())
                    v.setPadding(bars.left, bars.top, bars.right, bars.bottom)
                } else {
                    @Suppress("DEPRECATION")
                    v.setPadding(
                        insets.systemWindowInsetLeft, insets.systemWindowInsetTop,
                        insets.systemWindowInsetRight, insets.systemWindowInsetBottom,
                    )
                }
                insets
            }
        }
    }

    /**
     * An address to skip discovery with, when it has been set.
     *
     * Discovery works — confirmed on Android 16 — so this stays empty in normal
     * use. It is the escape hatch if a network ever blocks the broadcast.
     */
    private fun savedDeviceAddress(): String? =
        getSharedPreferences("ckkaraoke", Context.MODE_PRIVATE)
            .getString("device_address", null)

    /**
     * Back means one step back, and Android has two ways of saying it.
     *
     * Targeting SDK 36 opts the app into predictive back, and on Android 16 that
     * stops onBackPressed being called and stops KEYCODE_BACK being dispatched
     * at all. With nothing registered, the system's own default handling
     * finishes the Activity — so backing out of an artist's songs closed the app
     * instead of returning to the list. androidx's OnBackPressedDispatcher
     * covers both eras in one call; this app takes no dependencies, so both are
     * wired by hand.
     *
     * The page gets asked first, and that is why this is asynchronous. The
     * folder picker is a modal swapped in by htmx with no history entry behind
     * it, so going back from it would leave the page it is sitting on rather
     * than shutting it. Clicking its backdrop is exactly what tapping outside
     * the sheet does, so how it closes stays defined in the one place. The
     * element only exists while the sheet is open, which makes the null check
     * the test for whether it is.
     *
     * Nothing may act before evaluateJavascript answers: finishing early would
     * exit the app with the sheet still on screen.
     */
    private fun handleBack() {
        val w = web ?: run { finish(); return }
        w.evaluateJavascript(DISMISS_SHEET) { dismissed ->
            if (dismissed != "true") {
                if (w.canGoBack()) w.goBack() else finish()
            }
        }
    }

    // The path for devices below API 33, where there is no dispatcher to
    // register with. Dead code from Android 16 on.
    @Deprecated("Framework back handling", ReplaceWith("finish()"))
    @Suppress("DEPRECATION")
    override fun onBackPressed() = handleBack()

    /**
     * The registered callback, held so it can be handed back to unregister.
     *
     * A class of its own so that android.window is named nowhere in
     * MainActivity's own descriptors: it is loaded the first time it is used,
     * which only happens inside the version check, so an Oreo phone — minSdk is
     * 26 — never has to resolve a package it does not have.
     */
    @TargetApi(Build.VERSION_CODES.TIRAMISU)
    private class WebBack(private val activity: MainActivity) {
        private val callback = OnBackInvokedCallback { activity.handleBack() }

        fun register() = activity.onBackInvokedDispatcher
            .registerOnBackInvokedCallback(OnBackInvokedDispatcher.PRIORITY_DEFAULT, callback)

        fun unregister() = activity.onBackInvokedDispatcher
            .unregisterOnBackInvokedCallback(callback)
    }

    private var back: WebBack? = null

    /**
     * Points back handling at the WebView while there is one, and lets it go.
     *
     * Registration follows the WebView, not its history: it stays in place at
     * the first page and the callback calls finish() there, which is what
     * super.onBackPressed() did before. Registering only while canGoBack() is
     * true would keep the system's back-to-home preview, but it would have to
     * catch every history change to stay honest — including the sheet, which
     * arrives through an htmx swap and reaches no WebViewClient callback at all
     * — and every moment the two disagreed would be this same bug, arriving
     * intermittently. The preview is the price.
     */
    private fun bindWeb(w: WebView?) {
        web = w
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.TIRAMISU) return
        if (w != null) {
            // Guarded because a second showWeb — onResume can order one — must
            // not leave two callbacks on the dispatcher, each moving a page.
            if (back == null) back = WebBack(this).also { it.register() }
        } else {
            back?.unregister()
            back = null
        }
    }

    /**
     * Lets go of the WebView and the thread that was waiting on the server.
     *
     * Both outlived this Activity before. A WebView that is dropped without
     * destroy() keeps its renderer and its open event stream; and the waiter
     * sleeps in a loop for up to WAIT_LIMIT_MS — three minutes — holding a
     * main.post closure over an Activity that is already gone.
     */
    override fun onDestroy() {
        val w = web
        bindWeb(null)
        discard(w)
        waiting?.interrupt()
        waiting = null
        super.onDestroy()
    }

    private companion object {
        /** Closes the folder picker if it is open, and says whether it was. */
        const val DISMISS_SHEET =
            "(function(){var b=document.querySelector('.sheet-backdrop');" +
                "if(!b){return false;}b.click();return true;})()"

        /** Only a backstop; the real signal is the process exiting. */
        const val WAIT_LIMIT_MS = 180_000L

        /** Matches the page background, so the inset areas are not a dark band. */
        const val PAGE_BACKGROUND = 0xFFFFFFFF.toInt()

        /** Identifies the camera prompt raised for the sync page's scanner. */
        const val REQ_CAMERA = 1

        /** Where to put a favorites backup, and which one to restore. */
        const val REQ_SAVE = 2
        const val REQ_OPEN = 3

        /** filename= out of a Content-Disposition header, quoted or not. */
        val FILENAME = Regex("""filename\*?=(?:UTF-8'')?("[^"]*"|[^;]*)""", RegexOption.IGNORE_CASE)

        // The web UI's own colours, so the startup screen and the page it hands
        // over to are the same app rather than two.
        const val TEXT_PRIMARY = 0xFF212529.toInt()
        const val TEXT_MUTED = 0xFF6C757D.toInt()
        const val ACCENT = 0xFF0D6EFD.toInt()
    }
}
