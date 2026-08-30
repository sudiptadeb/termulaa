package com.debkosh.termulaa.ui

import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import com.debkosh.termulaa.AppGraph
import com.debkosh.termulaa.data.MachinesRepository
import kotlinx.coroutines.Job
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch

class MachinesViewModel(private val graph: AppGraph) : ViewModel() {

    private val _state = MutableStateFlow(MachinesUiState(loading = true))
    val state: StateFlow<MachinesUiState> = _state

    private var pollJob: Job? = null
    private var lastGoodFetchMillis: Long? = null

    init {
        viewModelScope.launch {
            graph.store.watchEnabled.collect { on ->
                _state.value = _state.value.copy(watchOn = on)
            }
        }
        // Show the remembered table instantly while the first fetch runs.
        viewModelScope.launch {
            val cached = graph.repository.cachedRows()
            if (_state.value.loading && _state.value.rows.isEmpty()) {
                _state.value = _state.value.copy(rows = cached)
            }
        }
    }

    /** 10s auto-poll while the screen is resumed. */
    fun startPolling() {
        if (pollJob?.isActive == true) return
        pollJob = viewModelScope.launch {
            while (isActive) {
                refreshInternal(pull = false)
                delay(10_000)
            }
        }
    }

    fun stopPolling() {
        pollJob?.cancel()
        pollJob = null
    }

    fun refresh() {
        viewModelScope.launch { refreshInternal(pull = true) }
    }

    private suspend fun refreshInternal(pull: Boolean) {
        if (pull) _state.value = _state.value.copy(refreshing = true)
        val snap = graph.repository.refresh()
        val banner = when (snap.state) {
            MachinesRepository.FetchState.OK -> null
            MachinesRepository.FetchState.SIGNED_OUT -> MachinesBanner.SignedOut
            MachinesRepository.FetchState.RC_DISABLED -> MachinesBanner.RcDisabled
            MachinesRepository.FetchState.VIEW_HOST ->
                MachinesBanner.ViewHost(snap.viewHost ?: "")
            MachinesRepository.FetchState.NETWORK_ERROR ->
                MachinesBanner.NetworkError(lastGoodFetchMillis)
        }
        if (snap.state == MachinesRepository.FetchState.OK) {
            lastGoodFetchMillis = snap.fetchedAtMillis
        }
        _state.value = _state.value.copy(
            loading = false,
            refreshing = false,
            rows = snap.rows,
            banner = banner,
            nowMillis = System.currentTimeMillis(),
        )
    }

    fun markSeen(machineId: String) {
        viewModelScope.launch {
            graph.repository.markSeen(machineId)
            graph.notifier.cancelMachine(machineId)
            refreshInternal(pull = false)
        }
    }

    fun setMuted(machineId: String, mute: Boolean) {
        viewModelScope.launch {
            graph.store.setNotifyEnabled(machineId, enabled = !mute)
            refreshInternal(pull = false)
        }
    }

    fun forget(machineId: String) {
        viewModelScope.launch {
            graph.store.forgetMachine(machineId)
            graph.notifier.cancelMachine(machineId)
            refreshInternal(pull = false)
        }
    }

    fun setWatchEnabled(on: Boolean) {
        viewModelScope.launch { graph.store.setWatchEnabled(on) }
    }
}
