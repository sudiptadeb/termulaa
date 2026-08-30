package com.debkosh.termulaa.watch

import android.app.Notification
import android.app.PendingIntent
import android.app.Service
import android.content.Context
import android.content.Intent
import android.content.pm.ServiceInfo
import android.os.Build
import android.os.IBinder
import androidx.core.app.NotificationCompat
import androidx.core.app.ServiceCompat
import com.debkosh.termulaa.AppGraph
import com.debkosh.termulaa.MainActivity
import com.debkosh.termulaa.R
import com.debkosh.termulaa.notify.AndroidNotifier
import com.debkosh.termulaa.work.CheckRunner
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.delay
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch

/**
 * Foreground (dataSync) live-watch service: runs the same check as the
 * periodic worker every 45 s while the Machines-screen toggle is on.
 * Survives app swipe-away (START_STICKY); stops itself once zero machines
 * have been online for 30 consecutive minutes.
 */
class WatchService : Service() {

    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.Default)
    private var loop: Job? = null

    override fun onBind(intent: Intent?): IBinder? = null

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        if (intent?.action == ACTION_STOP) {
            stopSelf()
            return START_NOT_STICKY
        }
        startAsForeground(watchingText(null))
        if (loop == null) loop = scope.launch { runLoop() }
        return START_STICKY
    }

    private fun startAsForeground(text: String) {
        // AndroidNotifier's constructor creates channels idempotently.
        AndroidNotifier(this)
        try {
            ServiceCompat.startForeground(
                this,
                AndroidNotifier.ID_WATCH_FOREGROUND,
                buildNotification(text),
                if (Build.VERSION.SDK_INT >= 29) ServiceInfo.FOREGROUND_SERVICE_TYPE_DATA_SYNC else 0,
            )
        } catch (_: Exception) {
            // ForegroundServiceStartNotAllowedException and friends: die quietly
            // rather than crash; the periodic worker still covers the user.
            stopSelf()
        }
    }

    private fun buildNotification(text: String): Notification {
        val open = PendingIntent.getActivity(
            this, 0,
            Intent(this, MainActivity::class.java),
            PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT,
        )
        return NotificationCompat.Builder(this, AndroidNotifier.CHANNEL_WATCH)
            .setSmallIcon(R.drawable.ic_stat_termulaa)
            .setContentTitle("Live watch")
            .setContentText(text)
            .setOngoing(true)
            .setContentIntent(open)
            .setOnlyAlertOnce(true)
            .setPriority(NotificationCompat.PRIORITY_LOW)
            .build()
    }

    private fun watchingText(count: Int?): String = when (count) {
        null -> "Watching — checking every 45s"
        0 -> "No machines online — checking every 45s"
        1 -> "Watching 1 machine — checking every 45s"
        else -> "Watching $count machines — checking every 45s"
    }

    private suspend fun runLoop() {
        val graph = AppGraph.get(applicationContext)
        var zeroOnlineSinceMillis: Long? = null
        while (scope.isActive) {
            val result = try {
                graph.checkRunner.runOnce()
            } catch (_: Exception) {
                CheckRunner.RunResult.NetworkError // belt and braces: never crash the loop
            }
            val online = (result as? CheckRunner.RunResult.Done)?.onlineCount
            updateNotification(watchingText(online))

            val now = System.currentTimeMillis()
            zeroOnlineSinceMillis = when {
                online == null -> zeroOnlineSinceMillis // errors don't count either way
                online == 0 -> zeroOnlineSinceMillis ?: now
                else -> null
            }
            if (zeroOnlineSinceMillis != null && now - zeroOnlineSinceMillis >= IDLE_STOP_MILLIS) {
                graph.store.setWatchEnabled(false)
                stopSelf()
                return
            }
            if (result is CheckRunner.RunResult.SignedOut) {
                // Re-login already failed inside the client; watching is pointless.
                graph.store.setWatchEnabled(false)
                stopSelf()
                return
            }
            delay(INTERVAL_MILLIS)
        }
    }

    private fun updateNotification(text: String) {
        try {
            val nm = getSystemService(Context.NOTIFICATION_SERVICE) as android.app.NotificationManager
            nm.notify(AndroidNotifier.ID_WATCH_FOREGROUND, buildNotification(text))
        } catch (_: Exception) {
            // Notification update is cosmetic; never crash the loop for it.
        }
    }

    override fun onDestroy() {
        loop = null
        scope.cancel()
        super.onDestroy()
    }

    companion object {
        const val INTERVAL_MILLIS = 45_000L
        const val IDLE_STOP_MILLIS = 30L * 60_000L
        private const val ACTION_STOP = "com.debkosh.termulaa.watch.STOP"

        fun start(context: Context) {
            try {
                androidx.core.content.ContextCompat.startForegroundService(
                    context, Intent(context, WatchService::class.java),
                )
            } catch (_: Exception) {
                // Background-start restrictions: ignore, the toggle flips back
                // next time the screen resumes and reads watchEnabled.
            }
        }

        fun stop(context: Context) {
            // stopService matches by Intent *equality*, not identity — a fresh
            // Intent here is correct; the lint heuristic misfires on it.
            @Suppress("ImplicitSamInstance")
            context.stopService(Intent(context, WatchService::class.java))
        }
    }
}
