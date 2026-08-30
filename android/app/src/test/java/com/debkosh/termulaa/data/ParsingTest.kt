package com.debkosh.termulaa.data

import kotlinx.serialization.builtins.ListSerializer
import kotlinx.serialization.serializer
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

/** Wire-shape parsing: exact contract JSON, unknown keys, absent fields. */
class ParsingTest {

    @Test
    fun `agents response parses with unknown keys`() {
        val json = """
            {"agents":[
                {"id":"a1b2c3d4","label":"buildbox","port":22,"tunnels":2,
                 "connected_at":"2026-08-30T09:00:00Z",
                 "url":"/rc/t/0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef/",
                 "some_future_field":true}
            ],"view_host":"","extra":{"nested":1}}
        """.trimIndent()
        val resp = WireJson.decodeFromString(serializer<AgentsResponse>(), json)
        assertEquals(1, resp.agents.size)
        assertEquals("a1b2c3d4", resp.agents[0].id)
        assertEquals("buildbox", resp.agents[0].label)
        assertEquals(2, resp.agents[0].tunnels)
        assertEquals("", resp.view_host)
        assertTrue(resp.agents[0].url!!.startsWith("/rc/t/"))
    }

    @Test
    fun `agent url may be absent in view-host mode`() {
        val json = """{"agents":[{"id":"a1b2c3d4","label":"x"}],"view_host":"view.example.com"}"""
        val resp = WireJson.decodeFromString(serializer<AgentsResponse>(), json)
        assertNull(resp.agents[0].url)
        assertEquals("view.example.com", resp.view_host)
    }

    @Test
    fun `tabs parse with last_output present`() {
        val json = """
            [{"id":"t1","name":"agent-run","pane_count":2,"alive":true,
              "last_active":"2026-08-30T09:10:00Z","last_output":"2026-08-30T09:12:00Z"}]
        """.trimIndent()
        val tabs = WireJson.decodeFromString(ListSerializer(serializer<TabInfo>()), json)
        assertEquals(1, tabs.size)
        assertEquals("2026-08-30T09:12:00Z", tabs[0].last_output)
        assertEquals(2, tabs[0].pane_count)
    }

    @Test
    fun `tabs parse with last_output ABSENT — older agent`() {
        val json = """
            [{"id":"t1","name":"old","pane_count":1,"alive":true,
              "last_active":"2026-08-30T09:10:00Z"}]
        """.trimIndent()
        val tabs = WireJson.decodeFromString(ListSerializer(serializer<TabInfo>()), json)
        assertNull(tabs[0].last_output)
        // latestOutput must report "unknown", not "never"
        val (latest, unknown) = latestOutput(tabs)
        assertNull(latest)
        assertTrue(unknown)
    }

    @Test
    fun `latestOutput picks the max across tabs and honors never sentinel`() {
        val tabs = listOf(
            TabInfo(id = "a", last_output = "2026-08-30T09:00:00Z"),
            TabInfo(id = "b", last_output = "2026-08-30T11:00:00Z"),
            TabInfo(id = "c", last_output = "0001-01-01T00:00:00Z"), // never
        )
        val (latest, unknown) = latestOutput(tabs)
        assertEquals(false, unknown)
        assertEquals(
            java.time.Instant.parse("2026-08-30T11:00:00Z").toEpochMilli(),
            latest,
        )
    }

    @Test
    fun `latestOutput with only never sentinels is null but known`() {
        val tabs = listOf(TabInfo(id = "a", last_output = "0001-01-01T00:00:00Z"))
        val (latest, unknown) = latestOutput(tabs)
        assertNull(latest)
        assertEquals(false, unknown)
    }

    @Test
    fun `empty tab list is known-nothing not unknown`() {
        val (latest, unknown) = latestOutput(emptyList())
        assertNull(latest)
        assertEquals(false, unknown)
    }

    @Test
    fun `session parses signed-in and signed-out`() {
        val signedIn = """{"auth":{"oidc_enabled":false},"features":{"rc":true},"user":{"name":"deb"}}"""
        val signedOut = """{"auth":{"oidc_enabled":true},"features":{"rc":false},"user":null}"""
        val a = WireJson.decodeFromString(serializer<SessionInfo>(), signedIn)
        val b = WireJson.decodeFromString(serializer<SessionInfo>(), signedOut)
        assertTrue(a.signedIn); assertTrue(a.rcEnabled)
        assertTrue(!b.signedIn); assertTrue(!b.rcEnabled)
    }

    @Test(expected = Exception::class)
    fun `html body fails to decode as agents`() {
        WireJson.decodeFromString(serializer<AgentsResponse>(), "<!doctype html><html></html>")
    }
}
