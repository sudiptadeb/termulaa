package com.debkosh.termulaa.data

import com.debkosh.termulaa.net.AgentsResult
import com.debkosh.termulaa.net.MemdClient
import com.debkosh.termulaa.net.MemdError
import com.debkosh.termulaa.net.Outcome
import com.debkosh.termulaa.store.AppStore

/**
 * Merges the live /rc/api/agents list with the locally-remembered machine
 * table for the Machines screen, and keeps the remembered table fresh.
 * Foreground refreshes never notify — the user is looking at the screen.
 */
class MachinesRepository(
    private val client: MemdClient,
    private val store: AppStore,
    private val clock: () -> Long = System::currentTimeMillis,
) {

    enum class FetchState { OK, SIGNED_OUT, RC_DISABLED, VIEW_HOST, NETWORK_ERROR }

    data class Snapshot(
        val rows: List<MachineRow>,
        val state: FetchState,
        val viewHost: String? = null,
        val fetchedAtMillis: Long? = null,
    )

    /** Rows from the remembered table only — the stale/offline fallback. */
    suspend fun cachedRows(): List<MachineRow> =
        mergeForUi(store.machinesNow(), liveAgents = null, tabsById = emptyMap(), seenWatermarks = store.seenNow())

    suspend fun refresh(): Snapshot {
        val agents: List<Agent> = when (val out = client.agents()) {
            is Outcome.Ok -> when (val r = out.value) {
                is AgentsResult.Machines -> r.agents
                is AgentsResult.ViewHostMode ->
                    return Snapshot(cachedRows(), FetchState.VIEW_HOST, viewHost = r.viewHost)
            }
            is Outcome.Err -> return when (out.error) {
                is MemdError.SignedOut -> Snapshot(cachedRows(), FetchState.SIGNED_OUT)
                is MemdError.NotJson -> disambiguateNotJson()
                else -> Snapshot(cachedRows(), FetchState.NETWORK_ERROR)
            }
        }

        // Tabs for every online machine (meta line + unseen pill).
        val tabsById = HashMap<String, TabsOutcome>()
        for (agent in agents) {
            val url = agent.url ?: continue
            tabsById[agent.id] = when (val t = client.tabs(url)) {
                is Outcome.Ok -> TabsOutcome.Got(t.value)
                is Outcome.Err -> TabsOutcome.Unavailable
            }
        }

        val remembered = store.machinesNow()
        val seen = store.seenNow()

        // Persist the refreshed table (labels, online flags, lastKnownOutput)
        // but do NOT touch lastNotified and do NOT emit notifications here.
        val check = computeCheck(
            CheckInput(
                remembered = remembered,
                liveAgents = agents,
                tabsById = tabsById,
                seenWatermarks = seen,
                lastNotified = emptyMap(),
                nowMillis = clock(),
            )
        )
        store.setMachines(check.newRemembered)

        val rows = mergeForUi(check.newRemembered, agents, tabsById, seen)
        return Snapshot(rows, FetchState.OK, fetchedAtMillis = clock())
    }

    /**
     * /rc/api/agents answered HTML: either rc is disabled (SPA fallthrough)
     * or we're signed out and got the login page. Ask /api/session.
     */
    private suspend fun disambiguateNotJson(): Snapshot {
        return when (val s = client.session()) {
            is Outcome.Ok -> when {
                !s.value.signedIn -> Snapshot(cachedRows(), FetchState.SIGNED_OUT)
                !s.value.rcEnabled -> Snapshot(cachedRows(), FetchState.RC_DISABLED)
                else -> Snapshot(cachedRows(), FetchState.NETWORK_ERROR)
            }
            is Outcome.Err -> when (s.error) {
                is MemdError.SignedOut -> Snapshot(cachedRows(), FetchState.SIGNED_OUT)
                else -> Snapshot(cachedRows(), FetchState.NETWORK_ERROR)
            }
        }
    }

    suspend fun markSeen(machineId: String) = store.markSeen(machineId, clock())
}
