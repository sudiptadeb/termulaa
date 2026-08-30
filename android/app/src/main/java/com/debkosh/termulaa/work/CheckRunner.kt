package com.debkosh.termulaa.work

import com.debkosh.termulaa.data.CheckInput
import com.debkosh.termulaa.data.TabsOutcome
import com.debkosh.termulaa.data.computeCheck
import com.debkosh.termulaa.net.AgentsResult
import com.debkosh.termulaa.net.MemdClient
import com.debkosh.termulaa.net.Outcome
import com.debkosh.termulaa.notify.Notifier
import com.debkosh.termulaa.store.AppStore

/**
 * One background check, shared verbatim by the 15-min WorkManager schedule
 * and the 45s foreground WatchService loop:
 * fetch agents → per online+notify-enabled machine fetch tabs → diff → notify
 * → persist. All decision logic lives in the pure [computeCheck].
 */
class CheckRunner(
    private val client: MemdClient,
    private val store: AppStore,
    private val notifier: Notifier,
    private val clock: () -> Long = System::currentTimeMillis,
) {

    sealed class RunResult {
        /** Check completed; onlineCount for the watch service notification. */
        data class Done(val onlineCount: Int) : RunResult()
        object SignedOut : RunResult()
        object NetworkError : RunResult()
        /** rc disabled / view-host mode / no server — nothing to check. */
        object NotApplicable : RunResult()
    }

    suspend fun runOnce(): RunResult {
        if (store.serverUrlNow() == null) return RunResult.NotApplicable

        val agents = when (val out = client.agents()) {
            is Outcome.Ok -> when (val r = out.value) {
                is AgentsResult.Machines -> r.agents
                is AgentsResult.ViewHostMode -> return RunResult.NotApplicable
            }
            is Outcome.Err -> return when (out.error) {
                is com.debkosh.termulaa.net.MemdError.SignedOut -> RunResult.SignedOut
                is com.debkosh.termulaa.net.MemdError.NotJson -> RunResult.NotApplicable // rc off
                is com.debkosh.termulaa.net.MemdError.Network -> RunResult.NetworkError
                else -> RunResult.NetworkError
            }
        }

        val remembered = store.machinesNow()
        val notifyById = remembered.associate { it.id to it.notifyEnabled }

        // Tabs only for machines whose notifications are enabled (default on).
        val tabsById = HashMap<String, TabsOutcome>()
        for (agent in agents) {
            if (notifyById[agent.id] == false) continue
            val url = agent.url ?: continue
            tabsById[agent.id] = when (val t = client.tabs(url)) {
                is Outcome.Ok -> TabsOutcome.Got(t.value)
                is Outcome.Err -> TabsOutcome.Unavailable
            }
        }

        val result = computeCheck(
            CheckInput(
                remembered = remembered,
                liveAgents = agents,
                tabsById = tabsById,
                seenWatermarks = store.seenNow(),
                lastNotified = store.lastNotifiedNow(),
                nowMillis = clock(),
            )
        )

        for (e in result.unseen) notifier.notifyUnseenOutput(e.machineId, e.label, e.outputAt)
        if (store.offlineAlertsNow()) {
            for (e in result.offline) notifier.notifyOffline(e.machineId, e.label, e.lastSeenOnline)
        }

        store.setMachines(result.newRemembered)
        store.setLastNotified(result.newLastNotified)
        return RunResult.Done(onlineCount = agents.size)
    }
}
