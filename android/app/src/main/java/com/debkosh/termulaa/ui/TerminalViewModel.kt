package com.debkosh.termulaa.ui

import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import com.debkosh.termulaa.AppGraph
import com.debkosh.termulaa.core.Urls
import com.debkosh.termulaa.net.AgentsResult
import com.debkosh.termulaa.net.Outcome
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.launch

sealed class TerminalState {
    object Loading : TerminalState()
    data class Ready(val label: String, val url: String) : TerminalState()
    /** Machine not in the live list (offline, or process-death restore raced it). */
    data class Gone(val label: String) : TerminalState()
    data class Failed(val message: String) : TerminalState()
}

class TerminalViewModel(private val graph: AppGraph) : ViewModel() {

    private val _state = MutableStateFlow<TerminalState>(TerminalState.Loading)
    val state: StateFlow<TerminalState> = _state

    /**
     * Resolve machineId → live agent URL. Done fresh on every entry (nav args
     * survive process death as just the id string, so everything else must be
     * re-derivable).
     */
    fun load(machineId: String) {
        viewModelScope.launch {
            val base = graph.currentBase()
            if (base == null) {
                _state.value = TerminalState.Failed("No server configured")
                return@launch
            }
            when (val out = graph.client.agents()) {
                is Outcome.Ok -> when (val r = out.value) {
                    is AgentsResult.Machines -> {
                        val agent = r.agents.firstOrNull { it.id == machineId }
                        val url = agent?.url
                        if (agent == null || url == null) {
                            val label = agent?.label
                                ?: graph.store.machinesNow().firstOrNull { it.id == machineId }?.label
                                ?: machineId
                            _state.value = TerminalState.Gone(label)
                        } else {
                            // Cookie must be in the WebView jar before loadUrl.
                            graph.syncCookieToWebView()
                            markSeen(machineId)
                            _state.value = TerminalState.Ready(agent.label, Urls.join(base, url))
                        }
                    }
                    is AgentsResult.ViewHostMode ->
                        _state.value = TerminalState.Failed(
                            "This server uses a dedicated view host; not yet supported by the app"
                        )
                }
                is Outcome.Err ->
                    _state.value = TerminalState.Failed("Can't reach the server")
            }
        }
    }

    /**
     * Watermark = now + clear notifications; called on open AND on leave.
     * Runs on the app-level scope: on leave the backstack entry (and this
     * ViewModel's scope) is already being torn down, and the write must not
     * be lost with it.
     */
    fun markSeen(machineId: String) {
        graph.scope.launch {
            graph.repository.markSeen(machineId)
            graph.notifier.cancelMachine(machineId)
        }
    }

    /** WebView main-frame 401: one re-login, cookie re-sync, retry signal. */
    suspend fun reloginForWebView(): Boolean {
        val ok = graph.client.reloginNow()
        if (ok) graph.syncCookieToWebView()
        return ok
    }
}
