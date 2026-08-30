package com.debkosh.termulaa.data

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * Table-driven tests for the unseen/offline diff — the heart of the
 * notification pipeline.
 */
class DiffTest {

    private val now = 1_756_500_000_000L // fixed clock
    private val t1 = now - 60_000        // a minute ago
    private val t2 = now - 30_000        // 30s ago

    private fun agent(id: String, label: String = "m-$id") =
        Agent(id = id, label = label, url = "/rc/t/${"0".repeat(64)}/")

    private fun tabsAt(vararg epochMillis: Long): TabsOutcome.Got =
        TabsOutcome.Got(epochMillis.map {
            TabInfo(id = "t$it", last_output = java.time.Instant.ofEpochMilli(it).toString())
        })

    private fun run(
        remembered: List<RememberedMachine> = emptyList(),
        live: List<Agent> = emptyList(),
        tabs: Map<String, TabsOutcome> = emptyMap(),
        seen: Map<String, Long> = emptyMap(),
        notified: Map<String, Long> = emptyMap(),
    ) = computeCheck(CheckInput(remembered, live, tabs, seen, notified, now))

    // ── unseen output ──────────────────────────────────────────────────────

    @Test
    fun `new output beyond watermark notifies`() {
        val r = run(
            live = listOf(agent("aa11bb22")),
            tabs = mapOf("aa11bb22" to tabsAt(t2)),
            seen = mapOf("aa11bb22" to t1),
        )
        assertEquals(1, r.unseen.size)
        assertEquals("aa11bb22", r.unseen[0].machineId)
        assertEquals(t2, r.unseen[0].outputAt)
        assertEquals(t2, r.newLastNotified["aa11bb22"])
    }

    @Test
    fun `output before watermark stays silent`() {
        val r = run(
            live = listOf(agent("aa11bb22")),
            tabs = mapOf("aa11bb22" to tabsAt(t1)),
            seen = mapOf("aa11bb22" to t2),
        )
        assertTrue(r.unseen.isEmpty())
    }

    @Test
    fun `already-notified output does not re-notify`() {
        val r = run(
            live = listOf(agent("aa11bb22")),
            tabs = mapOf("aa11bb22" to tabsAt(t2)),
            seen = mapOf("aa11bb22" to t1),
            notified = mapOf("aa11bb22" to t2),
        )
        assertTrue(r.unseen.isEmpty())
    }

    @Test
    fun `newer output than last notification notifies again`() {
        val r = run(
            live = listOf(agent("aa11bb22")),
            tabs = mapOf("aa11bb22" to tabsAt(t2)),
            seen = emptyMap(),
            notified = mapOf("aa11bb22" to t1),
        )
        assertEquals(1, r.unseen.size)
    }

    @Test
    fun `muted machine never notifies but still remembered`() {
        val r = run(
            remembered = listOf(RememberedMachine("aa11bb22", notifyEnabled = false, wasOnline = true)),
            live = listOf(agent("aa11bb22")),
            tabs = mapOf("aa11bb22" to tabsAt(t2)),
        )
        assertTrue(r.unseen.isEmpty())
        assertEquals(t2, r.newRemembered.single().lastKnownOutput)
    }

    @Test
    fun `machine without last_output field is unknown — no notifications`() {
        val oldAgentTabs = TabsOutcome.Got(listOf(TabInfo(id = "t", last_output = null)))
        val r = run(
            live = listOf(agent("aa11bb22")),
            tabs = mapOf("aa11bb22" to oldAgentTabs),
        )
        assertTrue(r.unseen.isEmpty())
        assertTrue(r.newRemembered.single().outputUnknown)
        assertNull(r.newRemembered.single().lastKnownOutput)
    }

    @Test
    fun `tabs fetch failure leaves output state untouched`() {
        val r = run(
            remembered = listOf(
                RememberedMachine("aa11bb22", wasOnline = true, lastKnownOutput = t1)
            ),
            live = listOf(agent("aa11bb22")),
            tabs = mapOf("aa11bb22" to TabsOutcome.Unavailable),
        )
        assertTrue(r.unseen.isEmpty())
        assertEquals(t1, r.newRemembered.single().lastKnownOutput)
    }

    @Test
    fun `lastKnownOutput never moves backwards`() {
        val r = run(
            remembered = listOf(
                RememberedMachine("aa11bb22", wasOnline = true, lastKnownOutput = t2)
            ),
            live = listOf(agent("aa11bb22")),
            tabs = mapOf("aa11bb22" to tabsAt(t1)), // older than remembered
        )
        assertEquals(t2, r.newRemembered.single().lastKnownOutput)
    }

    // ── offline transitions ────────────────────────────────────────────────

    @Test
    fun `remembered-online machine that vanished emits offline event once`() {
        val remembered = listOf(
            RememberedMachine("aa11bb22", label = "buildbox", wasOnline = true, lastSeenOnline = t1)
        )
        val r1 = run(remembered = remembered, live = emptyList())
        assertEquals(1, r1.offline.size)
        assertEquals("aa11bb22", r1.offline[0].machineId)
        assertEquals(t1, r1.offline[0].lastSeenOnline)
        assertFalse(r1.newRemembered.single().wasOnline)

        // Second check with the persisted state: no duplicate event.
        val r2 = run(remembered = r1.newRemembered, live = emptyList())
        assertTrue(r2.offline.isEmpty())
    }

    @Test
    fun `machine coming back online clears the offline flag`() {
        val remembered = listOf(RememberedMachine("aa11bb22", wasOnline = false, lastSeenOnline = t1))
        val r = run(remembered = remembered, live = listOf(agent("aa11bb22")))
        assertTrue(r.offline.isEmpty())
        assertTrue(r.newRemembered.single().wasOnline)
        assertEquals(now, r.newRemembered.single().lastSeenOnline)
    }

    @Test
    fun `brand new machine is remembered without events`() {
        val r = run(live = listOf(agent("ff00ff00", label = "fresh")))
        assertTrue(r.offline.isEmpty())
        assertTrue(r.unseen.isEmpty())
        val m = r.newRemembered.single()
        assertEquals("ff00ff00", m.id)
        assertEquals("fresh", m.label)
        assertTrue(m.wasOnline)
    }

    @Test
    fun `live label churn updates the remembered label`() {
        val remembered = listOf(RememberedMachine("aa11bb22", label = "old-name", wasOnline = true))
        val r = run(remembered = remembered, live = listOf(agent("aa11bb22", label = "new-name")))
        assertEquals("new-name", r.newRemembered.single().label)
    }

    @Test
    fun `agent list churn during diffing — one vanishes while another appears`() {
        val remembered = listOf(
            RememberedMachine("aaaaaaaa", wasOnline = true, lastSeenOnline = t1),
            RememberedMachine("bbbbbbbb", wasOnline = false, lastSeenOnline = t1),
        )
        val r = run(remembered = remembered, live = listOf(agent("cccccccc")))
        assertEquals(1, r.offline.size)                    // only aaaaaaaa transitions
        assertEquals("aaaaaaaa", r.offline[0].machineId)
        assertEquals(3, r.newRemembered.size)              // all three retained
        assertEquals(
            setOf("aaaaaaaa", "bbbbbbbb", "cccccccc"),
            r.newRemembered.map { it.id }.toSet(),
        )
    }

    // ── UI merge ───────────────────────────────────────────────────────────

    @Test
    fun `mergeForUi orders online first then most-recently-seen offline`() {
        val remembered = listOf(
            RememberedMachine("offold00", wasOnline = false, lastSeenOnline = t1 - 100),
            RememberedMachine("offnew00", wasOnline = false, lastSeenOnline = t1),
            RememberedMachine("online00", wasOnline = true, lastSeenOnline = now),
        )
        val rows = mergeForUi(
            remembered,
            liveAgents = listOf(agent("online00")),
            tabsById = mapOf("online00" to tabsAt(t2)),
            seenWatermarks = emptyMap(),
        )
        assertEquals(listOf("online00", "offnew00", "offold00"), rows.map { it.id })
        assertTrue(rows[0].online)
        assertTrue(rows[0].unseen) // t2 > absent watermark
        assertFalse(rows[1].online)
        assertEquals(t1, rows[1].offlineSince)
    }

    @Test
    fun `mergeForUi with null live list shows remembered as offline`() {
        val remembered = listOf(RememberedMachine("aa11bb22", wasOnline = true, lastSeenOnline = t1))
        val rows = mergeForUi(remembered, liveAgents = null, tabsById = emptyMap(), seenWatermarks = emptyMap())
        assertEquals(1, rows.size)
        assertFalse(rows[0].online)
    }

    @Test
    fun `unseen pill respects the seen watermark`() {
        val rows = mergeForUi(
            emptyList(),
            liveAgents = listOf(agent("aa11bb22")),
            tabsById = mapOf("aa11bb22" to tabsAt(t1)),
            seenWatermarks = mapOf("aa11bb22" to t2), // seen AFTER the output
        )
        assertFalse(rows[0].unseen)
    }
}
