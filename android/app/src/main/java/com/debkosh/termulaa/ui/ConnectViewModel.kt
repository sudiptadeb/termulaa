package com.debkosh.termulaa.ui

import android.os.Build
import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import com.debkosh.termulaa.AppGraph
import com.debkosh.termulaa.core.PairLink
import com.debkosh.termulaa.core.Urls
import com.debkosh.termulaa.net.MemdClient
import com.debkosh.termulaa.net.MemdError
import com.debkosh.termulaa.net.Outcome
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.launch

data class ConnectState(
    val busy: Boolean = false,
    val error: String? = null,
    /** Banner text: "un-paired" notice or a deep-link server-switch warning. */
    val notice: String? = null,
    /** Deep-link prefills; null = leave the fields alone. */
    val prefillServer: String? = null,
    val prefillCode: String? = null,
)

class ConnectViewModel(private val graph: AppGraph) : ViewModel() {

    private val _state = MutableStateFlow(ConnectState(notice = graph.client.authNotice))
    val state: StateFlow<ConnectState> = _state

    /**
     * Primary path: validate URL → redeem the pairing code (stores the app
     * token; the response's Set-Cookie signs us in) → validate via
     * /api/session → persist the server. Nothing is persisted until the whole
     * chain succeeds. Pairing against a different server than the configured
     * one replaces the old sign-in (local state cleared on success).
     */
    fun pair(serverUrl: String, code: String, onSuccess: () -> Unit) {
        if (_state.value.busy) return
        val base = when (val r = Urls.normalizeBase(serverUrl)) {
            is Urls.BaseResult.Ok -> r.base
            is Urls.BaseResult.Invalid -> {
                _state.value = _state.value.copy(busy = false, error = r.reason)
                return
            }
        }
        if (MemdClient.normalizeCode(code).isEmpty()) {
            _state.value = _state.value.copy(
                busy = false,
                error = "Enter the pairing code from your dashboard",
            )
            return
        }
        val previousBase = graph.currentBase()
        _state.value = _state.value.copy(busy = true, error = null)
        viewModelScope.launch {
            graph.setBase(base) // in-memory only until the redeem succeeds
            when (val redeem = graph.client.redeem(code, deviceLabel())) {
                is Outcome.Err -> {
                    graph.setBase(previousBase)
                    _state.value = _state.value.copy(
                        busy = false,
                        error = describePairError(redeem.error),
                    )
                    return@launch
                }
                is Outcome.Ok -> Unit
            }
            when (val session = graph.client.session()) {
                is Outcome.Err -> {
                    graph.client.clearAuth()
                    graph.setBase(previousBase)
                    _state.value = _state.value.copy(
                        busy = false,
                        error = describe(session.error, forLogin = false),
                    )
                }
                is Outcome.Ok -> {
                    // Switching servers: the old server's local state (machine
                    // table, watermarks) belongs to the old account — drop it.
                    val previousPersisted = graph.store.serverUrlNow()
                    if (previousPersisted != null && previousPersisted != base) {
                        graph.store.clearAll()
                    }
                    graph.store.setServerUrl(base)
                    _state.value = ConnectState()
                    onSuccess()
                }
            }
        }
    }

    /**
     * Secondary path (local accounts): validate URL → login → validate via
     * /api/session → persist server+creds. Unchanged v1.0 behavior.
     */
    fun connect(serverUrl: String, username: String, password: String, onSuccess: () -> Unit) {
        if (_state.value.busy) return
        val base = when (val r = Urls.normalizeBase(serverUrl)) {
            is Urls.BaseResult.Ok -> r.base
            is Urls.BaseResult.Invalid -> {
                _state.value = _state.value.copy(busy = false, error = r.reason)
                return
            }
        }
        if (username.isBlank() || password.isEmpty()) {
            _state.value = _state.value.copy(
                busy = false,
                error = "Enter your username and password",
            )
            return
        }
        _state.value = _state.value.copy(busy = true, error = null)
        viewModelScope.launch {
            graph.setBase(base) // in-memory only until the login succeeds
            when (val login = graph.client.login(username, password)) {
                is Outcome.Err -> {
                    graph.setBase(null)
                    _state.value = _state.value.copy(
                        busy = false,
                        error = describe(login.error, forLogin = true),
                    )
                    return@launch
                }
                is Outcome.Ok -> Unit
            }
            // Store creds BEFORE the session probe so its transparent
            // re-login path works, then validate.
            graph.client.storeCredentials(username, password)
            when (val session = graph.client.session()) {
                is Outcome.Err -> {
                    graph.client.clearAuth()
                    graph.setBase(null)
                    _state.value = _state.value.copy(
                        busy = false,
                        error = describe(session.error, forLogin = false),
                    )
                }
                is Outcome.Ok -> {
                    graph.store.setServerUrl(base)
                    _state.value = ConnectState()
                    onSuccess()
                }
            }
        }
    }

    /**
     * A termulaa://pair deep link arrived. Same/no server → prefill and
     * auto-redeem; different server → prefill plus a plainly-worded switch
     * notice, and wait for the user to tap Pair.
     */
    fun applyPairLink(uri: String, onSuccess: () -> Unit) {
        when (val d = PairLink.decide(PairLink.parse(uri), graph.currentBase())) {
            is PairLink.Decision.AutoPair -> {
                _state.value = _state.value.copy(
                    prefillServer = d.server,
                    prefillCode = d.code,
                    notice = null,
                    error = null,
                )
                pair(d.server, d.code, onSuccess)
            }
            is PairLink.Decision.PrefillOnly -> {
                _state.value = _state.value.copy(
                    prefillServer = d.server,
                    prefillCode = d.code,
                    notice = d.message,
                    error = null,
                )
            }
            PairLink.Decision.Ignore -> Unit
        }
    }

    private fun describe(e: MemdError, forLogin: Boolean): String = when (e) {
        is MemdError.Http ->
            if (e.code == 401 && forLogin) "Wrong username or password"
            else "Server error (HTTP ${e.code})"
        is MemdError.Network -> "Can't reach the server — check the URL and your connection"
        MemdError.NotJson -> "That URL doesn't look like a memd server"
        is MemdError.SignedOut -> "Sign-in failed"
        MemdError.NoServer -> "Enter a server URL"
        is MemdError.BadPayload -> "Unexpected response from the server"
    }
}

/** Human messages for the redeem step; pure so the mapping is unit-testable. */
internal fun describePairError(e: MemdError): String = when (e) {
    is MemdError.Http ->
        if (e.code == 401) {
            "That pairing code is invalid or expired — get a fresh one from your dashboard"
        } else {
            "Server error (HTTP ${e.code})"
        }
    is MemdError.Network -> "Can't reach the server — check the URL and your connection"
    MemdError.NotJson -> "That URL doesn't look like a memd server"
    is MemdError.SignedOut -> "Pairing failed — get a fresh code from your dashboard"
    MemdError.NoServer -> "Enter a server URL"
    is MemdError.BadPayload -> "Unexpected response from the server"
}

/** "Google Pixel 8" etc., trimmed to the contract's 64-char label cap. */
internal fun deviceLabel(): String {
    val manufacturer = Build.MANUFACTURER ?: ""
    val model = Build.MODEL ?: ""
    return "$manufacturer $model".trim().take(64).ifBlank { "Android phone" }
}
