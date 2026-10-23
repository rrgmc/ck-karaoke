package com.rrgmc.ckkaraoke

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.app.Service
import android.content.Context
import android.content.Intent
import android.graphics.drawable.Icon
import android.os.Build
import android.os.Handler
import android.os.IBinder
import android.os.Looper
import android.util.Log

/**
 * Keeps the server alive while the app is not in front — for a while.
 *
 * Without a foreground service the process is a candidate for being killed as
 * soon as the activity stops, which would drop the RakNet session — and a
 * dropped session means the device stops showing this phone as connected and
 * every queued song has to wait for a reconnect. Someone putting their phone
 * down mid-song is the normal case, not the exception.
 *
 * But "a while" is the whole of it: held forever, this keeps a Go process
 * talking to the karaoke unit, and a WebView swapping DOM, for a phone nobody
 * is holding. So backgrounding starts a countdown — see [appBackgrounded] —
 * and the session is given up when it runs out. That is the same bargain
 * iOS strikes in AppDelegate.applicationDidEnterBackground, reached from the
 * other side: iOS must ask for the time and is given a little, while here the
 * time is ours to take and the discipline has to be self-imposed.
 */
class ServerService : Service() {

    /**
     * Goes foreground, or gives up quietly rather than taking the app down.
     *
     * startForeground throws if the system does not believe this service is
     * allowed to start one — which is what happens when Android restarts a
     * killed service while the app is in the background. Left uncaught, the
     * exception escapes onCreate and the process dies, and with START_STICKY
     * the system would retry forever. START_NOT_STICKY below removes the loop;
     * this removes the crash, for any other path that reaches the same place.
     */
    override fun onCreate() {
        super.onCreate()
        try {
            startForeground(NOTIFICATION_ID, notification())
        } catch (e: Exception) {
            // Matched by name rather than with `is`. The class is API 31 and
            // minSdk is 26, so naming it would put an unresolvable type in this
            // method's descriptors on the phones least able to spare it — the
            // same care WebBack takes in MainActivity for android.window. The
            // name is unique, and this way there is no version check to keep in
            // step with it either.
            if (e.javaClass.name == FGS_NOT_ALLOWED) {
                Log.w(Server.TAG, "not allowed to go foreground here; not starting", e)
                stopSelf()
                return
            }
            // Anything else is a real fault and should be as loud as it was.
            // A SecurityException here means the manifest's foregroundServiceType
            // is missing a permission it demands, which must not be swallowed.
            throw e
        }
        instance = this
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        if (intent?.action == ACTION_STOP) {
            stopSelf()
            return START_NOT_STICKY
        }
        Server.start(this, intent?.getStringExtra(EXTRA_ADDRESS))
        cancelGrace()

        // Never START_STICKY. A service the system resurrects comes back with
        // the app in the background, and a foreground service may not be
        // started from there — so startForeground throws and the process dies.
        // There is nothing to resurrect it *for*, either: the server exists to
        // serve a WebView that is no longer on screen. MainActivity.onStart
        // starts it again, from the foreground, where it is legal.
        return START_NOT_STICKY
    }

    /**
     * Swiping the app away is a clearer "I am done" than backgrounding it, so
     * the session ends at once rather than waiting out the grace period.
     */
    override fun onTaskRemoved(rootIntent: Intent?) {
        stopSelf()
        super.onTaskRemoved(rootIntent)
    }

    /**
     * Stops the server. Server.stop no longer blocks — see the note there on
     * why the five-second wait moved off the caller's thread.
     */
    override fun onDestroy() {
        cancelGrace()
        if (instance === this) instance = null
        Server.stop()
        super.onDestroy()
    }

    override fun onBind(intent: Intent?): IBinder? = null

    private fun cancelGrace() = grace.removeCallbacksAndMessages(null)

    private fun notification(): Notification {
        val manager = getSystemService(Context.NOTIFICATION_SERVICE) as NotificationManager
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            manager.createNotificationChannel(
                NotificationChannel(
                    CHANNEL, getString(R.string.notification_title), NotificationManager.IMPORTANCE_LOW,
                )
                    .apply { description = getString(R.string.channel_description) }
            )
        }

        val open = PendingIntent.getActivity(
            this, 0, Intent(this, MainActivity::class.java),
            PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT,
        )

        val builder = if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            Notification.Builder(this, CHANNEL)
        } else {
            @Suppress("DEPRECATION")
            Notification.Builder(this)
        }

        // Somewhere to stop it from. Without this the only ways out were swiping
        // the app away or force-stopping it, and a force stop cannot disconnect
        // cleanly — the unit then shows a phantom client until it times out.
        val stop = PendingIntent.getService(
            this, 1,
            Intent(this, ServerService::class.java).setAction(ACTION_STOP),
            PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT,
        )

        return builder
            .setContentTitle(getString(R.string.notification_title))
            .setContentText(getString(R.string.notification_text))
            .setSmallIcon(android.R.drawable.ic_media_play)
            .setContentIntent(open)
            .setOngoing(true)
            .addAction(
                Notification.Action.Builder(
                    Icon.createWithResource(this, android.R.drawable.ic_menu_close_clear_cancel),
                    getString(R.string.stop), stop,
                ).build()
            )
            .build()
    }

    companion object {
        const val EXTRA_ADDRESS = "device_address"
        const val ACTION_STOP = "com.rrgmc.ckkaraoke.STOP"
        private const val CHANNEL = "ckkaraoke-server"
        private const val NOTIFICATION_ID = 1

        /** android.app.ForegroundServiceStartNotAllowedException — see onCreate. */
        private const val FGS_NOT_ALLOWED =
            "android.app.ForegroundServiceStartNotAllowedException"

        /**
         * How long the session outlives the app leaving the screen.
         *
         * Long enough that nothing ordinary reaches it — answering a message,
         * looking up lyrics, taking a call all cost far less — and short enough
         * that a phone put down for the evening is not still talking to the
         * karaoke unit at midnight. Coming back after this costs a restart and
         * a reload, which is why it is not tighter: the server binds port 0, so
         * a restart moves the port and the page has to be loaded again anyway.
         */
        private const val GRACE_MS = 10 * 60 * 1000L

        /**
         * The running service, for the Activity to speak to directly.
         *
         * Same process, so this needs no Intent and no binding — and an Intent
         * would not do: starting a service from onStop is itself a background
         * start, which is the thing this whole file is about not doing. Held
         * only between onCreate and onDestroy, and cleared there.
         */
        private var instance: ServerService? = null

        private val grace = Handler(Looper.getMainLooper())

        /**
         * Starts the countdown to giving the session up.
         *
         * Called when the app stops being visible. If it comes back first,
         * [appForegrounded] cancels this and nothing was lost.
         */
        fun appBackgrounded() {
            val service = instance ?: return
            grace.removeCallbacksAndMessages(null)
            Log.i(Server.TAG, "app backgrounded; holding the session for ${GRACE_MS / 60000} min")
            grace.postDelayed({
                Log.i(Server.TAG, "grace period expired; stopping the server")
                service.stopSelf()
            }, GRACE_MS)
        }

        /** Cancels the countdown, the app being back on screen. */
        fun appForegrounded() {
            grace.removeCallbacksAndMessages(null)
        }

        /** Whether the server is up, so a returning Activity knows to restart it. */
        val isRunning: Boolean
            get() = instance != null && Server.isRunning

        fun start(context: Context, deviceAddress: String?) {
            val intent = Intent(context, ServerService::class.java)
                .putExtra(EXTRA_ADDRESS, deviceAddress)
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
                context.startForegroundService(intent)
            } else {
                context.startService(intent)
            }
        }
    }
}
