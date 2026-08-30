package com.debkosh.termulaa

import android.content.Context
import android.webkit.CookieManager
import com.debkosh.termulaa.data.MachinesRepository
import com.debkosh.termulaa.net.EncryptedSecretStore
import com.debkosh.termulaa.net.MemdClient
import com.debkosh.termulaa.net.MemdCookieJar
import com.debkosh.termulaa.notify.AndroidNotifier
import com.debkosh.termulaa.store.AppStore
import com.debkosh.termulaa.work.CheckRunner
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.launch
import kotlinx.coroutines.runBlocking

/**
 * Manual DI. One instance per process, reachable from activities, workers and
 * services via [AppGraph.get].
 */
class AppGraph private constructor(private val context: Context) {

    val scope = CoroutineScope(SupervisorJob() + Dispatchers.Default)

    val store = AppStore(context)

    private val secrets = EncryptedSecretStore(context)

    /**
     * The server base URL lives in DataStore (async) but OkHttp needs it
     * synchronously; this volatile mirror is kept in sync by the collector
     * below, with a one-time blocking read as the cold-start fallback (only
     * ever hit on a background/IO thread inside the client).
     */
    @Volatile private var baseCache: String? = null
    @Volatile private var baseLoaded = false

    init {
        scope.launch {
            store.serverUrl.collect {
                baseCache = it
                baseLoaded = true
            }
        }
    }

    fun currentBase(): String? {
        if (!baseLoaded) {
            baseCache = runBlocking { store.serverUrlNow() }
            baseLoaded = true
        }
        return baseCache
    }

    /** Sets the in-memory base immediately (Connect flow, pre-persist). */
    fun setBase(url: String?) {
        baseCache = url
        baseLoaded = true
    }

    /**
     * Cookie bridge, OkHttp jar → android.webkit.CookieManager: whenever the
     * memd_session cookie changes, mirror it for the base origin so the
     * WebView terminal shares the session. Guarded — the WebView provider can
     * be missing/broken on exotic devices and must never take the app down.
     */
    val cookieJar = MemdCookieJar(secrets) { url, header ->
        try {
            val cm = CookieManager.getInstance()
            if (header != null) {
                cm.setCookie(url.toString(), "$header; Path=/", null)
                cm.flush()
            }
        } catch (_: Throwable) {
            // No WebView / dead provider: API polling still works fine.
        }
    }

    val client = MemdClient(
        baseProvider = ::currentBase,
        creds = secrets,
        cookieJar = cookieJar,
    )

    /** Re-push the stored cookie into the WebView jar (Terminal screen open). */
    fun syncCookieToWebView() {
        val base = currentBase() ?: return
        val header = cookieJar.currentCookieHeader() ?: return
        try {
            val cm = CookieManager.getInstance()
            cm.setCookie(base, "$header; Path=/", null)
            cm.flush()
        } catch (_: Throwable) {
        }
    }

    val notifier: AndroidNotifier by lazy { AndroidNotifier(context) }

    val repository by lazy { MachinesRepository(client, store) }

    val checkRunner by lazy { CheckRunner(client, store, notifier) }

    companion object {
        // The graph holds only the APPLICATION context (see get()), which has
        // process lifetime — this is not the leak the lint check fears.
        @android.annotation.SuppressLint("StaticFieldLeak")
        @Volatile private var instance: AppGraph? = null

        fun get(context: Context): AppGraph =
            instance ?: synchronized(this) {
                instance ?: AppGraph(context.applicationContext).also { instance = it }
            }
    }
}
