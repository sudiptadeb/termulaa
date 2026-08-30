package com.debkosh.termulaa.data

import com.debkosh.termulaa.core.Times
import kotlinx.serialization.Serializable

/**
 * Pure diff/merge logic between the live agent list and the locally
 * remembered machine table. No Android types — everything here is
 * table-driven-testable on the JVM.
 *
 * Server truth: only currently-connected machines appear in /rc/api/agents;
 * a disconnected machine simply VANISHES. So "offline" is entirely a local
 * inference: remembered-as-online but now absent.
 */

/** Locally remembered machine, keyed by the 8-hex agent id. */
@Serializable
data class RememberedMachine(
    val id: String,
    val label: String = "",
    val wasOnline: Boolean = false,
    /** Epoch millis of the last poll that saw this machine online. */
    val lastSeenOnline: Long? = null,
    /** Epoch millis of the newest PTY output we ever observed. Null = none/unknown. */
    val lastKnownOutput: Long? = null,
    /** True when the machine's agent doesn't report last_output (older agent). */
    val outputUnknown: Boolean = false,
    val notifyEnabled: Boolean = true,
)

/** Result of fetching <agent.url>api/tabs for one machine during a check. */
sealed class TabsOutcome {
    data class Got(val tabs: List<TabInfo>) : TabsOutcome()
    /** Fetch failed (network/http) — leave remembered output state untouched. */
    object Unavailable : TabsOutcome()
}

/**
 * Newest last_output across tabs (epoch millis), plus whether the field was
 * missing on every tab (older termulaa agent ⇒ activity unknown).
 * Returns (null, true) for "unknown", (null, false) for "never yet".
 */
fun latestOutput(tabs: List<TabInfo>): Pair<Long?, Boolean> {
    if (tabs.isEmpty()) return Pair(null, false)
    var sawField = false
    var latest: Long? = null
    for (t in tabs) {
        if (t.last_output != null) {
            sawField = true
            val inst = Times.parseOrNever(t.last_output)
            if (inst != null) {
                val ms = inst.toEpochMilli()
                if (latest == null || ms > latest) latest = ms
            }
        }
    }
    return if (!sawField) Pair(null, true) else Pair(latest, false)
}

data class UnseenEvent(
    val machineId: String,
    val label: String,
    val outputAt: Long,
)

data class OfflineEvent(
    val machineId: String,
    val label: String,
    val lastSeenOnline: Long?,
)

data class CheckInput(
    val remembered: List<RememberedMachine>,
    val liveAgents: List<Agent>,
    /** Per online machine id; missing key = tabs were not fetched (e.g. muted). */
    val tabsById: Map<String, TabsOutcome>,
    /** Per machine id, epoch millis last time the user "saw" the machine. */
    val seenWatermarks: Map<String, Long>,
    /** Per machine id, epoch millis of the newest output already notified. */
    val lastNotified: Map<String, Long>,
    val nowMillis: Long,
)

data class CheckResult(
    val newRemembered: List<RememberedMachine>,
    val unseen: List<UnseenEvent>,
    val offline: List<OfflineEvent>,
    /** Full replacement map for the lastNotified store. */
    val newLastNotified: Map<String, Long>,
)

/**
 * One background/foreground check step. Deterministic; the caller supplies
 * everything (including now) and persists/notifies from the result.
 */
fun computeCheck(input: CheckInput): CheckResult {
    val liveById = input.liveAgents.associateBy { it.id }
    val rememberedById = input.remembered.associateBy { it.id }
    val newRemembered = LinkedHashMap<String, RememberedMachine>()
    val unseen = ArrayList<UnseenEvent>()
    val offline = ArrayList<OfflineEvent>()
    val newLastNotified = HashMap(input.lastNotified)

    // Pass 1: every live agent → remembered (created or refreshed).
    for (agent in input.liveAgents) {
        val prev = rememberedById[agent.id]
        var m = (prev ?: RememberedMachine(id = agent.id)).copy(
            label = agent.label,
            wasOnline = true,
            lastSeenOnline = input.nowMillis,
        )
        when (val outcome = input.tabsById[agent.id]) {
            is TabsOutcome.Got -> {
                val (latest, unknown) = latestOutput(outcome.tabs)
                m = m.copy(outputUnknown = unknown)
                if (!unknown && latest != null) {
                    // Never move lastKnownOutput backwards (agent restarts etc).
                    if (m.lastKnownOutput == null || latest > m.lastKnownOutput!!) {
                        m = m.copy(lastKnownOutput = latest)
                    }
                    val watermark = input.seenWatermarks[agent.id] ?: 0L
                    val already = input.lastNotified[agent.id] ?: 0L
                    if (m.notifyEnabled && latest > watermark && latest > already) {
                        unseen.add(UnseenEvent(agent.id, agent.label, latest))
                        newLastNotified[agent.id] = latest
                    }
                }
            }
            TabsOutcome.Unavailable, null -> Unit // leave output state as-is
        }
        newRemembered[agent.id] = m
    }

    // Pass 2: remembered machines that are now absent.
    for (m in input.remembered) {
        if (liveById.containsKey(m.id)) continue
        if (m.wasOnline) {
            // online → gone transition detected on THIS check.
            offline.add(OfflineEvent(m.id, m.label, m.lastSeenOnline))
            newRemembered[m.id] = m.copy(wasOnline = false)
        } else {
            newRemembered[m.id] = m // stays offline, no repeat event
        }
    }

    return CheckResult(newRemembered.values.toList(), unseen, offline, newLastNotified)
}

/** Row model the Machines screen renders. */
data class MachineRow(
    val id: String,
    val label: String,
    val online: Boolean,
    val agentUrl: String?,
    val tabCount: Int?,
    val lastOutput: Long?,
    val outputUnknown: Boolean,
    val unseen: Boolean,
    val offlineSince: Long?,
    val notifyEnabled: Boolean,
)

/**
 * Merge for the UI list: online machines first (server order), then
 * remembered-but-gone machines (most recently seen first).
 */
fun mergeForUi(
    remembered: List<RememberedMachine>,
    liveAgents: List<Agent>?,
    tabsById: Map<String, TabsOutcome>,
    seenWatermarks: Map<String, Long>,
): List<MachineRow> {
    val rememberedById = remembered.associateBy { it.id }
    val rows = ArrayList<MachineRow>()
    val liveIds = HashSet<String>()
    if (liveAgents != null) {
        for (agent in liveAgents) {
            liveIds.add(agent.id)
            val mem = rememberedById[agent.id]
            val tabs = (tabsById[agent.id] as? TabsOutcome.Got)?.tabs
            val (latest, unknownNow) = if (tabs != null) latestOutput(tabs) else Pair(null, false)
            val lastOutput = latest ?: mem?.lastKnownOutput
            val unknown = if (tabs != null) unknownNow else (mem?.outputUnknown ?: false)
            val watermark = seenWatermarks[agent.id] ?: 0L
            rows.add(
                MachineRow(
                    id = agent.id,
                    label = agent.label,
                    online = true,
                    agentUrl = agent.url,
                    tabCount = tabs?.size,
                    lastOutput = lastOutput,
                    outputUnknown = unknown,
                    unseen = !unknown && lastOutput != null && lastOutput > watermark,
                    offlineSince = null,
                    notifyEnabled = mem?.notifyEnabled ?: true,
                )
            )
        }
    }
    val gone = remembered.filter { it.id !in liveIds }
        .sortedByDescending { it.lastSeenOnline ?: 0L }
    for (m in gone) {
        val watermark = seenWatermarks[m.id] ?: 0L
        rows.add(
            MachineRow(
                id = m.id,
                label = m.label,
                online = false,
                agentUrl = null,
                tabCount = null,
                lastOutput = m.lastKnownOutput,
                outputUnknown = m.outputUnknown,
                unseen = !m.outputUnknown && m.lastKnownOutput != null && m.lastKnownOutput > watermark,
                offlineSince = m.lastSeenOnline,
                notifyEnabled = m.notifyEnabled,
            )
        )
    }
    return rows
}
