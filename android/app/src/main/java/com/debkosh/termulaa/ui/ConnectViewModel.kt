package com.debkosh.termulaa.ui

import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import com.debkosh.termulaa.AppGraph
import com.debkosh.termulaa.core.Urls
import com.debkosh.termulaa.net.MemdError
import com.debkosh.termulaa.net.Outcome
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.launch

data class ConnectState(val busy: Boolean = false, val error: String? = null)

class ConnectViewModel(private val graph: AppGraph) : ViewModel() {

    private val _state = MutableStateFlow(ConnectState())
    val state: StateFlow<ConnectState> = _state

    /**
     * Validate URL → login → validate via /api/session → persist server+creds.
     * Nothing is persisted until the whole chain succeeds, so a failed first
     * connect leaves the app cleanly on the Connect screen.
     */
    fun connect(serverUrl: String, username: String, password: String, onSuccess: () -> Unit) {
        if (_state.value.busy) return
        val base = when (val r = Urls.normalizeBase(serverUrl)) {
            is Urls.BaseResult.Ok -> r.base
            is Urls.BaseResult.Invalid -> {
                _state.value = ConnectState(error = r.reason)
                return
            }
        }
        if (username.isBlank() || password.isEmpty()) {
            _state.value = ConnectState(error = "Enter your username and password")
            return
        }
        _state.value = ConnectState(busy = true)
        viewModelScope.launch {
            graph.setBase(base) // in-memory only until the login succeeds
            when (val login = graph.client.login(username, password)) {
                is Outcome.Err -> {
                    graph.setBase(null)
                    _state.value = ConnectState(error = describe(login.error, forLogin = true))
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
                    _state.value = ConnectState(error = describe(session.error, forLogin = false))
                }
                is Outcome.Ok -> {
                    graph.store.setServerUrl(base)
                    _state.value = ConnectState()
                    onSuccess()
                }
            }
        }
    }

    private fun describe(e: MemdError, forLogin: Boolean): String = when (e) {
        is MemdError.Http ->
            if (e.code == 401 && forLogin) "Wrong username or password"
            else "Server error (HTTP ${e.code})"
        is MemdError.Network -> "Can't reach the server — check the URL and your connection"
        MemdError.NotJson -> "That URL doesn't look like a memd server"
        MemdError.SignedOut -> "Sign-in failed"
        MemdError.NoServer -> "Enter a server URL"
        is MemdError.BadPayload -> "Unexpected response from the server"
    }
}
