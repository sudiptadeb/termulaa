package com.debkosh.termulaa.notify

import android.Manifest
import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.Context
import android.content.Intent
import android.content.pm.PackageManager
import androidx.core.app.NotificationCompat
import androidx.core.content.ContextCompat
import com.debkosh.termulaa.MainActivity
import com.debkosh.termulaa.R
import com.debkosh.termulaa.core.Times

/**
 * Notification surface used by both CheckWorker and WatchService. Interface
 * so the check runner is testable with a recording fake.
 */
interface Notifier {
    fun notifyUnseenOutput(machineId: String, label: String, outputAtMillis: Long)
    fun notifyOffline(machineId: String, label: String, lastSeenMillis: Long?)
    fun cancelMachine(machineId: String)
}

class AndroidNotifier(private val context: Context) : Notifier {

    private val nm: NotificationManager =
        context.getSystemService(Context.NOTIFICATION_SERVICE) as NotificationManager

    init {
        ensureChannels()
    }

    /** Channel creation is idempotent — safe to call on every construction. */
    private fun ensureChannels() {
        nm.createNotificationChannel(
            NotificationChannel(CHANNEL_OUTPUT, "Session output", NotificationManager.IMPORTANCE_DEFAULT)
                .apply { description = "A watched machine produced new terminal output" }
        )
        nm.createNotificationChannel(
            NotificationChannel(CHANNEL_STATUS, "Machine status", NotificationManager.IMPORTANCE_HIGH)
                .apply { description = "A machine went offline" }
        )
        nm.createNotificationChannel(
            NotificationChannel(CHANNEL_WATCH, "Live watch", NotificationManager.IMPORTANCE_LOW)
                .apply {
                    description = "Persistent status while live watch is on"
                    setShowBadge(false)
                }
        )
    }

    private fun canPost(): Boolean {
        // Below API 33 POST_NOTIFICATIONS does not exist and checkSelfPermission
        // for an unknown permission returns DENIED — so gate the check by SDK.
        if (android.os.Build.VERSION.SDK_INT < 33) return true
        return ContextCompat.checkSelfPermission(context, Manifest.permission.POST_NOTIFICATIONS) ==
            PackageManager.PERMISSION_GRANTED
    }

    /** Immutable deep-link into the machine's terminal screen. */
    private fun contentIntent(machineId: String): PendingIntent {
        val intent = Intent(context, MainActivity::class.java).apply {
            action = MainActivity.ACTION_OPEN_MACHINE
            putExtra(MainActivity.EXTRA_MACHINE_ID, machineId)
            flags = Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_CLEAR_TOP
        }
        return PendingIntent.getActivity(
            context,
            machineId.hashCode(), // distinct requestCode per machine
            intent,
            PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT,
        )
    }

    override fun notifyUnseenOutput(machineId: String, label: String, outputAtMillis: Long) {
        if (!canPost()) return
        val name = label.ifBlank { machineId }
        val ago = Times.relative(outputAtMillis, System.currentTimeMillis())
        val n = NotificationCompat.Builder(context, CHANNEL_OUTPUT)
            .setSmallIcon(R.drawable.ic_stat_termulaa)
            .setContentTitle(name)
            .setContentText("New session output · $ago")
            .setGroup(GROUP_OUTPUT)
            .setContentIntent(contentIntent(machineId))
            .setAutoCancel(true)
            .setOnlyAlertOnce(true)
            .setCategory(Notification.CATEGORY_STATUS)
            .build()
        // One notification per machine: stable tag ⇒ later output updates it.
        nm.notify(machineId, ID_OUTPUT, n)
    }

    override fun notifyOffline(machineId: String, label: String, lastSeenMillis: Long?) {
        if (!canPost()) return
        val name = label.ifBlank { machineId }
        val since = lastSeenMillis?.let { " · last seen ${Times.relative(it, System.currentTimeMillis())}" } ?: ""
        val n = NotificationCompat.Builder(context, CHANNEL_STATUS)
            .setSmallIcon(R.drawable.ic_stat_termulaa)
            .setContentTitle(name)
            .setContentText("Machine went offline$since")
            .setContentIntent(contentIntent(machineId))
            .setAutoCancel(true)
            .setCategory(Notification.CATEGORY_ERROR)
            .build()
        nm.notify(machineId, ID_STATUS, n)
    }

    override fun cancelMachine(machineId: String) {
        nm.cancel(machineId, ID_OUTPUT)
        nm.cancel(machineId, ID_STATUS)
    }

    companion object {
        const val CHANNEL_OUTPUT = "session_output"
        const val CHANNEL_STATUS = "machine_status"
        const val CHANNEL_WATCH = "live_watch"
        const val GROUP_OUTPUT = "com.debkosh.termulaa.OUTPUT"
        const val ID_OUTPUT = 1
        const val ID_STATUS = 2
        const val ID_WATCH_FOREGROUND = 100
    }
}
