package com.debkosh.termulaa.net

import com.debkosh.termulaa.core.Urls
import com.debkosh.termulaa.ui.describePairError
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

    // ── pairing: redeem / bearer re-auth / revoke ──────────────────────────

    private fun redeemOk(token: String = "mat_tok_1"): MockResponse =
        jsonResponse("""{"token":"$token","user":{"name":"deb"}}""")
            .setHeader("Set-Cookie", "memd_session=PAIRED; Path=/; HttpOnly; Max-Age=86400")

    private fun bearerSessionOk(): MockResponse =
        jsonResponse("""{"user":{"name":"deb"}}""")
            .setHeader("Set-Cookie", "memd_session=COOKIE1; Path=/; HttpOnly; Max-Age=86400")

    private fun storeToken(token: String = "mat_tok_1") {
        secrets.put("creds.apptoken", token)
    }

    @Test
    fun `redeem posts the normalized code and label, stores token and cookie`() = runTest {
        server.enqueue(redeemOk())
        server.enqueue(jsonResponse(agentsBody))

        val out = client.redeem("abc-def ghj", "Google Pixel 8")
        assertTrue("expected Ok, got $out", out is Outcome.Ok)

        val req = server.takeRequest()
        assertEquals("POST", req.method)
        assertEquals("/api/app/redeem", req.path)
        assertNull(req.getHeader("Authorization")) // no auth on redeem
        val body = req.body.readUtf8()
        assertTrue(body, body.contains("\"code\":\"ABCDEFGHJ\""))
        assertTrue(body, body.contains("\"label\":\"Google Pixel 8\""))

        assertTrue(client.hasAppToken())
        // The Set-Cookie signed us in: the cookie rides the next request.
        client.agents()
        assertEquals("memd_session=PAIRED", server.takeRequest().getHeader("Cookie"))
    }

    @Test
    fun `redeem trims the device label to 64 chars`() = runTest {
        server.enqueue(redeemOk())
        client.redeem("ABCDEFGHJ", "x".repeat(90))
        val body = server.takeRequest().body.readUtf8()
        assertTrue(body, body.contains("\"label\":\"${"x".repeat(64)}\""))
    }

    @Test
    fun `redeem 401 surfaces the human invalid-code message and stores nothing`() = runTest {
        server.enqueue(jsonResponse("""{"error":"invalid or expired code"}""", code = 401))
        val out = client.redeem("WRO-NGC-ODE", "Phone")
        assertTrue(out is Outcome.Err)
        val err = (out as Outcome.Err).error
        assertEquals(401, (err as MemdError.Http).code)
        assertEquals(
            "That pairing code is invalid or expired — get a fresh one from your dashboard",
            describePairError(err),
        )
        assertTrue(!client.hasAppToken())
        assertNull(cookieJar.currentCookieHeader())
    }

    @Test
    fun `redeem 200 without a token is BadPayload`() = runTest {
        server.enqueue(jsonResponse("""{"user":{"name":"deb"}}"""))
        val out = client.redeem("ABCDEFGHJ", "Phone")
        assertTrue((out as Outcome.Err).error is MemdError.BadPayload)
        assertTrue(!client.hasAppToken())
    }

    @Test
    fun `401 recovery prefers the bearer token over stored password creds`() = runTest {
        client.storeCredentials("deb", "hunter2") // both present → token must win
        storeToken("mat_tok_1")
        server.enqueue(jsonResponse("""{"error":"unauthorized"}""", code = 401)) // agents #1
        server.enqueue(bearerSessionOk())                                        // bearer re-auth
        server.enqueue(jsonResponse(agentsBody))                                 // agents #2

        val out = client.agents()
        assertTrue("expected Ok, got $out", out is Outcome.Ok)

        assertEquals("/rc/api/agents", server.takeRequest().path)
        val reauth = server.takeRequest()
        assertEquals("/api/app/session", reauth.path) // NOT /api/auth/login
        assertEquals("POST", reauth.method)
        assertEquals("Bearer mat_tok_1", reauth.getHeader("Authorization"))
        val retry = server.takeRequest()
        assertEquals("/rc/api/agents", retry.path)
        assertEquals("memd_session=COOKIE1", retry.getHeader("Cookie"))
        assertEquals(3, server.requestCount) // exactly one retry, no loop
    }

    @Test
    fun `bearer 401 is SignedOut with the un-paired notice and no password fallback`() = runTest {
        client.storeCredentials("deb", "hunter2") // present, but must NOT be tried
        storeToken()
        server.enqueue(jsonResponse("""{"error":"unauthorized"}""", code = 401)) // agents
        server.enqueue(jsonResponse("""{"error":"unauthorized"}""", code = 401)) // bearer

        val out = client.agents()
        val err = (out as Outcome.Err).error
        assertTrue(err is MemdError.SignedOut)
        assertEquals(MemdClient.UNPAIRED_NOTICE, (err as MemdError.SignedOut).notice)
        assertEquals(MemdClient.UNPAIRED_NOTICE, client.authNotice)
        assertEquals(2, server.requestCount) // definitive: no /api/auth/login attempt
    }

    @Test
    fun `password re-login still works when only creds are stored`() = runTest {
        // (regression guard for the local-account path — no token in the store)
        client.storeCredentials("deb", "hunter2")
        server.enqueue(jsonResponse("""{"error":"unauthorized"}""", code = 401))
        server.enqueue(loginOk())
        server.enqueue(jsonResponse(agentsBody))
        assertTrue(client.agents() is Outcome.Ok)
        server.takeRequest()
        assertEquals("/api/auth/login", server.takeRequest().path)
    }

    @Test
    fun `signOut revokes the token then clears all auth`() = runTest {
        storeToken("mat_tok_1")
        secrets.put("cookie.value", "C")
        secrets.put("cookie.host", server.url("/").host)
        server.enqueue(jsonResponse("{}"))

        client.signOut()

        val req = server.takeRequest()
        assertEquals("DELETE", req.method)
        assertEquals("/api/app/tokens/self", req.path)
        assertEquals("Bearer mat_tok_1", req.getHeader("Authorization"))
        assertTrue(!client.hasAppToken())
        assertNull(cookieJar.currentCookieHeader())
        assertNull(client.authNotice)
    }

    @Test
    fun `signOut still clears auth when the revoke fails`() = runTest {
        storeToken()
        server.enqueue(jsonResponse("""{"error":"boom"}""", code = 500))
        client.signOut()
        assertEquals(1, server.requestCount)
        assertTrue(!client.hasAppToken())
    }

    @Test
    fun `signOut still clears auth when the server is unreachable`() = runTest {
        storeToken()
        server.shutdown() // connection refused → IOException inside revokeSelf
        client.signOut()
        assertTrue(!client.hasAppToken())
        assertTrue(!client.hasCredentials())
    }

    @Test
    fun `signOut without a token makes no network call`() = runTest {
        client.storeCredentials("deb", "hunter2")
        client.signOut()
        assertEquals(0, server.requestCount)
        assertTrue(!client.hasCredentials())
    }

    @Test
    fun `pairing code normalization is forgiving`() {
        val cases = listOf(
            "ABC-DEF-GHJ" to "ABCDEFGHJ",
            "abc-def-ghj" to "ABCDEFGHJ",
            "AbC dEf GhJ" to "ABCDEFGHJ",
            "  abcdefghj\t" to "ABCDEFGHJ",
            "a-b-c-d-e-f-g-h-j" to "ABCDEFGHJ",
            "ABCDEFGHJ" to "ABCDEFGHJ",
            "234-567-89X" to "23456789X",
            "" to "",
            "---" to "",
        )
        for ((raw, want) in cases) {
            assertEquals("normalizeCode(\"$raw\")", want, MemdClient.normalizeCode(raw))
        }
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
