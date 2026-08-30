package com.debkosh.termulaa

import android.content.Intent
import android.os.Bundle
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import com.debkosh.termulaa.ui.TermulaaNav
import com.debkosh.termulaa.ui.TermulaaTheme
import kotlinx.coroutines.runBlocking

class MainActivity : ComponentActivity() {

    /** Machine id from a tapped notification, consumed by the nav host. */
    private var pendingMachineId by mutableStateOf<String?>(null)

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        val graph = AppGraph.get(this)

        pendingMachineId = extractMachineId(intent)

        // One tiny synchronous read to pick the start destination; the prefs
        // file is a few hundred bytes.
        val startSignedIn =
            runBlocking { graph.store.serverUrlNow() } != null && graph.client.hasCredentials()

        val versionName = try {
            packageManager.getPackageInfo(packageName, 0).versionName ?: "?"
        } catch (_: Exception) {
            "?"
        }

        setContent {
            TermulaaTheme {
                TermulaaNav(
                    graph = graph,
                    startSignedIn = startSignedIn,
                    versionName = versionName,
                    pendingMachineId = pendingMachineId,
                    onDeepLinkConsumed = { pendingMachineId = null },
                )
            }
        }
    }

    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        // singleTask: a notification tap while running lands here.
        extractMachineId(intent)?.let { pendingMachineId = it }
    }

    private fun extractMachineId(intent: Intent?): String? {
        if (intent?.action != ACTION_OPEN_MACHINE) return null
        return intent.getStringExtra(EXTRA_MACHINE_ID)?.takeIf { it.isNotBlank() }
    }

    companion object {
        const val ACTION_OPEN_MACHINE = "com.debkosh.termulaa.OPEN_MACHINE"
        const val EXTRA_MACHINE_ID = "machine_id"
    }
}
