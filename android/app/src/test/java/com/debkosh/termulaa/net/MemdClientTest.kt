package com.debkosh.termulaa.net

import com.debkosh.termulaa.core.Urls
import kotlinx.coroutines.test.runTest
import okhttp3.mockwebserver.MockResponse
import okhttp3.mockwebserver.MockWebServer
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Test

/**
 * MemdClient against MockWebServer: login + cookie persistence, the
 * 401 → re-login → retry-once rule, and defensive parsing.
 */
class MemdClientTest {

    private lateinit var server: MockWebServer
    private lateinit var secrets: InMemorySecretStore
    private lateinit var cookieJar: MemdCookieJar
    private lateinit var client: MemdClient
    private val cookieChanges = mutableListOf<String?>()

    @Before
    fun setUp() {
        server = MockWebServer()
        server.start()
        secrets = InMemorySecretStore()
        cookieJar = MemdCookieJar(secrets) { _, header -> cookieChanges.add(header) }
        val base = (Urls.normalizeBase(server.url("/").toString()) as Urls.BaseResult.Ok).base
        client = MemdClient(baseProvider = { base }, creds = secrets, cookieJar = cookieJar)
    }

    @After
    fun tearDown() {
        server.shutdown()
    }

    private fun jsonResponse(body: String, code: Int = 200): MockResponse =
        MockResponse().setResponseCode(code)
            .setHeader("Content-Type", "application/json")
            .setBody(body)

    private fun loginOk(): MockResponse =
        jsonResponse("""{"user":{"name":"deb"}}""")
            .setHeader("Set-Cookie", "memd_session=COOKIE1; Path=/; HttpOnly; Max-Age=86400")

    private val agentsBody =
        """{"agents":[{"id":"a1b2c3d4","label":"box","port":22,"tunnels":1,""" +
            """"connected_at":"2026-08-30T09:00:00Z","url":"/rc/t/${"a".repeat(64)}/"}],"view_host":""}"""

    // ── login + cookie ─────────────────────────────────────────────────────

    @Test
    fun `login posts credentials, stores cookie, and mirrors it`() = runTest {
        server.enqueue(loginOk())
        server.enqueue(jsonResponse(agentsBody))

        val out = client.login("deb", "hunter2")
        assertTrue(out is Outcome.Ok)

        val loginReq = server.takeRequest()
        assertEquals("POST", loginReq.method)
        assertEquals("/api/auth/login", loginReq.path)
        assertTrue(loginReq.body.readUtf8().contains("\"username\":\"deb\""))

        // Cookie mirrored toward the WebView bridge…
        assertEquals(listOf("memd_session=COOKIE1"), cookieChanges)

        // …and sent on the next request.
        val agents = client.agents()
        assertTrue(agents is Outcome.Ok)
        val agentsReq = server.takeRequest()
        assertEquals("/rc/api/agents", agentsReq.path)
        assertEquals("memd_session=COOKIE1", agentsReq.getHeader("Cookie"))
    }

    @Test
    fun `login with bad creds surfaces 401 without storing a cookie`() = runTest {
        server.enqueue(jsonResponse("""{"error":"bad credentials"}""", code = 401))
        val out = client.login("deb", "wrong")
        assertTrue(out is Outcome.Err)
        assertTrue((out as Outcome.Err).error is MemdError.Http)
        assertEquals(401, (out.error as MemdError.Http).code)
        assertNull(cookieJar.currentCookieHeader())
    }

    // ── 401 → re-login → retry once ────────────────────────────────────────

    @Test
    fun `expired cookie triggers one re-login then one retry`() = runTest {
        client.storeCredentials("deb", "hunter2")
        server.enqueue(jsonResponse("""{"error":"unauthorized"}""", code = 401)) // agents #1
        server.enqueue(loginOk())                                               // re-login
        server.enqueue(jsonResponse(agentsBody))                                // agents #2

        val out = client.agents()
        assertTrue("expected Ok, got $out", out is Outcome.Ok)
        val machines = (out as Outcome.Ok).value as AgentsResult.Machines
        assertEquals("a1b2c3d4", machines.agents.single().id)

        assertEquals("/rc/api/agents", server.takeRequest().path)
        assertEquals("/api/auth/login", server.takeRequest().path)
        val retry = server.takeRequest()
        assertEquals("/rc/api/agents", retry.path)
        assertEquals("memd_session=COOKIE1", retry.getHeader("Cookie"))
        assertEquals(3, server.requestCount) // exactly one retry, no loop
    }

    @Test
    fun `re-login failure surfaces SignedOut after exactly one attempt`() = runTest {
        client.storeCredentials("deb", "revoked")
        server.enqueue(jsonResponse("""{"error":"unauthorized"}""", code = 401)) // agents
        server.enqueue(jsonResponse("""{"error":"bad credentials"}""", code = 401)) // login

        val out = client.agents()
        assertTrue(out is Outcome.Err)
        assertTrue((out as Outcome.Err).error is MemdError.SignedOut)
        assertEquals(2, server.requestCount) // no retry after failed login
    }

    @Test
    fun `401 with no stored creds is SignedOut immediately`() = runTest {
        server.enqueue(jsonResponse("""{"error":"unauthorized"}""", code = 401))
        val out = client.agents()
        assertTrue((out as Outcome.Err).error is MemdError.SignedOut)
        assertEquals(1, server.requestCount) // no login attempt without creds
    }

    @Test
    fun `retry that still 401s is SignedOut, not a loop`() = runTest {
        client.storeCredentials("deb", "hunter2")
        server.enqueue(jsonResponse("{}", code = 401)) // agents #1
        server.enqueue(loginOk())                      // login "succeeds"
        server.enqueue(jsonResponse("{}", code = 401)) // agents #2 still 401

        val out = client.agents()
        assertTrue((out as Outcome.Err).error is MemdError.SignedOut)
        assertEquals(3, server.requestCount)
    }

    @Test
    fun `session with user null re-logins and retries`() = runTest {
        client.storeCredentials("deb", "hunter2")
        server.enqueue(jsonResponse("""{"auth":{"oidc_enabled":false},"features":{"rc":true},"user":null}"""))
        server.enqueue(loginOk())
        server.enqueue(jsonResponse("""{"auth":{"oidc_enabled":false},"features":{"rc":true},"user":{"n":"d"}}"""))

        val out = client.session()
        assertTrue(out is Outcome.Ok)
        assertTrue((out as Outcome.Ok).value.signedIn)
        assertEquals(3, server.requestCount)
    }

    // ── defensive parsing ──────────────────────────────────────────────────

    @Test
    fun `html fallthrough on agents is NotJson, not a crash`() = runTest {
        server.enqueue(
            MockResponse().setResponseCode(200)
                .setHeader("Content-Type", "text/html; charset=utf-8")
                .setBody("<!doctype html><html><body>SPA</body></html>")
        )
        val out = client.agents()
        assertTrue((out as Outcome.Err).error is MemdError.NotJson)
    }

    @Test
    fun `json content-type with html body is still NotJson`() = runTest {
        server.enqueue(jsonResponse("<!doctype html>"))
        val out = client.agents()
        assertTrue((out as Outcome.Err).error is MemdError.NotJson)
    }

    @Test
    fun `malformed json is BadPayload`() = runTest {
        server.enqueue(jsonResponse("""{"agents":"nope"}"""))
        val out = client.agents()
        assertTrue((out as Outcome.Err).error is MemdError.BadPayload)
    }

    @Test
    fun `server error code is typed`() = runTest {
        server.enqueue(jsonResponse("""{"error":"boom"}""", code = 503))
        val out = client.agents()
        assertEquals(503, ((out as Outcome.Err).error as MemdError.Http).code)
    }

    @Test
    fun `view_host mode is surfaced as its own state`() = runTest {
        server.enqueue(jsonResponse("""{"agents":[],"view_host":"view.example.com"}"""))
        val out = client.agents()
        val vh = (out as Outcome.Ok).value as AgentsResult.ViewHostMode
        assertEquals("view.example.com", vh.viewHost)
    }

    @Test
    fun `tabs URL is built from the agent url`() = runTest {
        val agentUrl = "/rc/t/${"a".repeat(64)}/"
        server.enqueue(jsonResponse("""[{"id":"t1","name":"x","pane_count":1,"alive":true,"last_active":"2026-08-30T09:00:00Z"}]"""))
        val out = client.tabs(agentUrl)
        assertTrue(out is Outcome.Ok)
        assertEquals("${agentUrl}api/tabs", server.takeRequest().path)
    }

    @Test
    fun `expired stored cookie is not sent`() = runTest {
        // Simulate a cookie that expired yesterday.
        secrets.put("cookie.value", "STALE")
        secrets.put("cookie.host", server.url("/").host)
        secrets.put("cookie.expires", (System.currentTimeMillis() - 86_400_000).toString())
        server.enqueue(jsonResponse(agentsBody))
        client.agents()
        assertNull(server.takeRequest().getHeader("Cookie"))
    }
}
