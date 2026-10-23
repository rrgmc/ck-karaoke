package com.rrgmc.ckkaraoke

import android.content.Context
import android.os.Build
import android.util.Log
import java.io.BufferedReader
import java.io.File
import java.io.InputStreamReader
import java.util.TimeZone
import java.util.concurrent.TimeUnit

/**
 * The Go server, running as a child process.
 *
 * It is shipped inside the APK as `libckkaraoke.so`. That name and location are
 * not cosmetic: only files matching `lib*.so` are extracted from the APK, and
 * the native library directory is the one place an app may still execute a file
 * from under the W^X rules Android has enforced since 10. A copy in filesDir
 * would be unrunnable.
 */
object Server {

    const val TAG = "ckkaraoke"

    /** The port the server bound, or 0 until it has told us. */
    @Volatile
    var port: Int = 0
        private set

    /** The last fatal error, for showing something better than a blank screen. */
    @Volatile
    var failure: String? = null
        private set

    private var process: Process? = null

    val isRunning: Boolean
        get() = process?.isAlive == true

    @Synchronized
    fun start(context: Context, deviceAddress: String?) {
        if (isRunning) return
        failure = null

        val binary = File(context.applicationInfo.nativeLibraryDir, "libckkaraoke.so")
        if (!binary.canExecute()) {
            failure = "the server binary is missing or not executable at ${binary.path}"
            Log.e(TAG, failure!!)
            return
        }

        val data = File(context.filesDir, "data").apply { mkdirs() }

        val args = mutableListOf(
            binary.absolutePath,
            // Loopback only. The phone's server is for this phone's WebView, and
            // binding the WiFi interface would put an unauthenticated remote
            // control on the network.
            "-listen", "127.0.0.1:0",
            "-data", data.absolutePath,
            // The device tags each reserved song with the session's name, so
            // this is what the TV shows beside songs queued here.
            "-name", Build.MODEL ?: "Android",
        )
        if (!deviceAddress.isNullOrBlank()) {
            args += listOf("-addr", deviceAddress)
        }

        Log.i(TAG, "starting: ${args.joinToString(" ")}")
        val started = try {
            val builder = ProcessBuilder(args).redirectErrorStream(true)
            passTimeZone(builder)
            builder.start()
        } catch (e: Exception) {
            failure = "could not start the server: ${e.message}"
            Log.e(TAG, failure!!, e)
            return
        }
        process = started

        // The server prints "listening 127.0.0.1:<port>" once bound, before it
        // serves anything. Reading for it beats polling for an open port, and
        // the rest of the output goes to the log so a failure is diagnosable.
        Thread({ readOutput(started) }, "ckkaraoke-server-output").apply {
            isDaemon = true
            start()
        }
    }

    /**
     * Tells the server which time zone the phone is in.
     *
     * Go does not work this out for itself on Android: `time`'s `initLocal` is,
     * in full, `localLoc = *UTC`, and nothing there reads `TZ`. Left alone the
     * server believes it is in UTC, so a backup saved in the evening can carry
     * the next day's date, and every line it logs disagrees with logcat.
     *
     * The offset is what the server actually uses, because resolving a zone name
     * needs a zoneinfo database and Android's has moved to an apex path that Go
     * no longer looks in. The name goes along only so the zone reads as
     * "America/Sao_Paulo" rather than "-03" wherever it is printed.
     *
     * Read at each start, so a phone that crosses a zone picks the new one up
     * the next time the app is opened.
     */
    private fun passTimeZone(builder: ProcessBuilder) {
        val zone = TimeZone.getDefault() ?: return
        val env = builder.environment()
        env["CK_TZ"] = zone.id
        env["CK_TZ_OFFSET"] = (zone.getOffset(System.currentTimeMillis()) / 1000).toString()
    }

    private fun readOutput(p: Process) {
        try {
            BufferedReader(InputStreamReader(p.inputStream)).use { reader ->
                while (true) {
                    val line = reader.readLine() ?: break
                    Log.i(TAG, line)
                    if (port == 0 && line.startsWith("listening ")) {
                        port = line.substringAfterLast(':').trim().toIntOrNull() ?: 0
                        Log.i(TAG, "server is on port $port")
                    }
                }
            }
        } catch (e: Exception) {
            // Expected on the way out, and not worth a stack trace there:
            // Process.destroy() closes all three streams immediately after
            // signalling, so this read fails while the server is still shutting
            // down cleanly. `process` is already null by then, since stop()
            // clears it before signalling — which is how a stop we asked for is
            // told apart from a pipe that broke on its own.
            val deliberate = synchronized(this) { process !== p }
            if (deliberate) {
                Log.i(TAG, "server output closed while stopping")
            } else {
                Log.w(TAG, "stopped reading server output", e)
            }
        }
        val code = try { p.waitFor() } catch (e: InterruptedException) { -1 }
        Log.w(TAG, "server exited with code $code")

        // A process we asked to stop is not a failure to report, and saying so
        // is not cosmetic: `stop` clears `port` before the process has died, so
        // the `port == 0` test below reads true for every deliberate stop and
        // would leave `failure` set. The next launch then has a moment where the
        // service has been asked to start but Server.start — which clears it —
        // has not run yet, because startForegroundService is asynchronous. That
        // is exactly when waitForServer looks, so returning to the app after the
        // grace period showed "the server exited before it started listening"
        // over a server that was starting perfectly well.
        //
        // `process` is the test because `stop` nulls it. Anything else that
        // reaches here is the server having died on its own, which is worth
        // reporting.
        synchronized(this) {
            if (process !== p) return
            if (port == 0) {
                failure = "the server exited before it started listening (code $code)"
            }
            process = null
            port = 0
        }
    }

    /** Waits up to [timeoutMs] for the port, returning 0 if it never arrives. */
    fun awaitPort(timeoutMs: Long): Int {
        val deadline = System.currentTimeMillis() + timeoutMs
        while (System.currentTimeMillis() < deadline) {
            val p = port
            if (p != 0) return p
            if (failure != null) return 0
            Thread.sleep(50)
        }
        return port
    }

    /**
     * Stops the server, giving it time to close its session properly.
     *
     * destroy() sends SIGTERM, which the server handles by disconnecting from
     * the karaoke device and waiting for the disconnect to go out. Skipping that
     * wait leaves the unit believing a client is still connected until its own
     * timeout expires, which is what "1 device connected" with nothing running
     * looks like.
     *
     * The wait is worth having and is not worth blocking on. Its only caller is
     * ServerService.onDestroy, which runs on the main looper and is reached from
     * onTaskRemoved — so five seconds here was five seconds of an unresponsive
     * app at the exact moment the user swiped it away, which is one of the ways
     * an app gets reported as hanging.
     *
     * The bookkeeping stays on the calling thread even so, and that part is not
     * optional: `port` must read 0 before this returns. Otherwise an Activity
     * coming back — MainActivity.onStart, after the grace period stopped the
     * server — can see the port of the process now being killed and load a page
     * against it. The next server binds a different port, because it asks for 0.
     */
    @Synchronized
    fun stop() {
        val p = process ?: return
        process = null
        port = 0

        // Not a daemon: it has to outlive the last Android component here, which
        // is the whole point of it.
        Thread({ shutdown(p) }, "ckkaraoke-server-stop").apply { isDaemon = false }.start()
    }

    private fun shutdown(p: Process) {
        p.destroy()
        try {
            if (!p.waitFor(SHUTDOWN_WAIT_SECONDS, TimeUnit.SECONDS)) {
                Log.w(TAG, "server did not exit in time; killing it")
                p.destroyForcibly()
            }
        } catch (e: InterruptedException) {
            p.destroyForcibly()
        }
    }

    /** Longer than the client's own 3s wait for the disconnect to be sent. */
    private const val SHUTDOWN_WAIT_SECONDS = 5L
}
