package com.rrgmc.ckkaraoke

import android.app.Activity
import android.content.Intent
import android.os.Bundle
import android.util.Log

/**
 * Favorites sent from the other app on this phone: CK Karaoke to CK Queue
 * Remote, or back.
 *
 * The two apps keep separate favorites. The sync and backup pages offer to
 * send a folder or everything to the other app, and that app's
 * MainActivity.sendToPeer starts this activity with the payload.
 *
 * Only an app signed with the same key can start it: the manifest guards it
 * with a signature permission that both apps declare. It shows nothing. It
 * hands the payload to MainActivity and finishes. MainActivity opens the page
 * that asks where to add the songs, so nothing is written without the user.
 *
 * A trampoline rather than an intent filter on MainActivity, because the
 * permission would then also guard the launcher entry, and because starting
 * MainActivity from here with CLEAR_TOP and SINGLE_TOP reaches the instance
 * that is already open instead of stacking a second WebView on it.
 */
class ReceiveFavorites : Activity() {

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        val incoming = Incoming.from(intent)
        if (incoming == null) {
            Log.w(Server.TAG, "favorites sent with nothing in them")
        } else {
            Incoming.pending = incoming
            startActivity(
                Intent(this, MainActivity::class.java).addFlags(
                    Intent.FLAG_ACTIVITY_NEW_TASK or
                        Intent.FLAG_ACTIVITY_CLEAR_TOP or
                        Intent.FLAG_ACTIVITY_SINGLE_TOP
                )
            )
        }
        finish()
    }
}

/** What the other app sent: a folder code, or a whole backup file. */
sealed class Incoming {
    data class Folder(val code: String) : Incoming()
    data class Backup(val xml: String) : Incoming()

    companion object {
        /** The action MainActivity.sendToPeer sends and ReceiveFavorites takes. */
        const val ACTION = "com.rrgmc.ck.action.SEND_FAVORITES"
        const val EXTRA_KIND = "kind"
        const val EXTRA_DATA = "data"
        const val KIND_FOLDER = "folder"
        const val KIND_BACKUP = "backup"

        /**
         * The largest backup sent inline, in characters.
         *
         * An intent travels through a binder buffer of about 1 MB shared by
         * the whole process. A backup of a thousand songs is under 100 KB, so
         * this leaves room; a larger one goes through Save a backup instead.
         */
        const val MAX_INLINE = 256 * 1024

        /** Sent and not yet shown. MainActivity takes it once the server runs. */
        @Volatile
        var pending: Incoming? = null

        fun from(intent: Intent?): Incoming? {
            if (intent?.action != ACTION) return null
            val data = intent.getStringExtra(EXTRA_DATA)
            if (data.isNullOrEmpty()) return null
            return when (intent.getStringExtra(EXTRA_KIND)) {
                KIND_FOLDER -> Folder(data)
                KIND_BACKUP -> Backup(data)
                else -> null
            }
        }
    }
}
