package com.debkosh.termulaa.net

import com.debkosh.termulaa.core.Urls
import com.debkosh.termulaa.data.Agent
import com.debkosh.termulaa.data.AgentsResponse
import com.debkosh.termulaa.data.LoginRequest
import com.debkosh.termulaa.data.SessionInfo
import com.debkosh.termulaa.data.TabInfo
import com.debkosh.termulaa.data.WireJson
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.runInterruptible
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock
import kotlinx.serialization.builtins.ListSerializer
import kotlinx.serialization.serializer
import okhttp3.HttpUrl.Companion.toHttpUrlOrNull
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.RequestBody.Companion.toRequestBody
import okhttp3.Response
import java.io.IOException
import java.util.concurrent.TimeUnit

/** Typed failures — every network parse is defensive, nothing here crashes. */
sealed class MemdError(message: String) : Exception(message) {
    class Network(cause: Throwable) :
        MemdError("network error: ${cause.message ?: cause.javaClass.simpleName}")
    /** 401 (or user:null) and no creds / re-login failed → show Connect. */
    object SignedOut : MemdError("signed out") { private fun readResolve(): Any = SignedOut }
    class Http(val code: Int) : MemdError("HTTP $code")
    /** HTML fallthrough / wrong content type where JSON was expected. */
    object NotJson : MemdError("response was not JSON") { private fun readResolve(): Any = NotJson }
    class BadPayload(cause: Throwable) :
        MemdError("bad payload: ${cause.message ?: cause.javaClass.simpleName}")
    object NoServer : MemdError("no server configured") { private fun readResolve(): Any = NoServer }
}

sealed class Outcome<out T> {
    data class Ok<T>(val value: T) : Outcome<T>()
    data class Err(val error: MemdError) : Outcome<Nothing>()

    fun valueOrNull(): T? = (this as? Ok)?.value
    inline fun <R> map(f: (T) -> R): Outcome<R> = when (this) {
        is Ok -> Ok(f(value))
        is Err -> this
    }
}

/** Agents fetch result: either the machine list (path mode) or a typed state. */
sealed class AgentsResult {
    data class Machines(val agents: List<Agent>) : AgentsResult()
    /** view_host non-empty — dedicated view hosts unsupported in v1. */
    data class ViewHostMode(val viewHost: String) : AgentsResult()
}

/**
 * The single OkHttp-backed client for the memd server. Owns the cookie jar
 * and the transparent re-login rule:
 *
 *   on any 401 (or /api/session with user:null), if credentials are stored,
 *   perform ONE login attempt and retry the original request ONCE; if that
 *   login fails → Outcome.Err(SignedOut).
 *
 * The memd_session cookie has a ~24h absolute TTL, so this path is ROUTINE.
 */
class MemdClient(
    private val baseProvider: () -> String?,
    private val creds: SecretStore,
    val cookieJar: MemdCookieJar,
    client: OkHttpClient? = null,
) {
    private val http: OkHttpClient = (client?.newBuilder() ?: OkHttpClient.Builder())
        .cookieJar(cookieJar)
        .connectTimeout(10, TimeUnit.SECONDS)
        .readTimeout(20, TimeUnit.SECONDS)
        .build()

    private val loginMutex = Mutex()

    // ── credentials ────────────────────────────────────────────────────────

    fun storeCredentials(username: String, password: String) {
        creds.put(KEY_USER, username)
        creds.put(KEY_PASS, password)
    }

    fun hasCredentials(): Boolean =
        creds.get(KEY_USER) != null && creds.get(KEY_PASS) != null

    fun clearAuth() {
        creds.put(KEY_USER, null)
        creds.put(KEY_PASS, null)
        cookieJar.clear()
    }

    // ── public API ─────────────────────────────────────────────────────────

    /** POST /api/auth/login with explicit credentials (Connect screen). */
    suspend fun login(username: String, password: String): Outcome<Unit> {
        val base = baseProvider() ?: return Outcome.Err(MemdError.NoServer)
        return loginRaw(base, username, password)
    }

    /** GET /api/session (with transparent re-login on user:null/401). */
    suspend fun session(): Outcome<SessionInfo> {
        val out = getJson("/api/session", serializer<SessionInfo>())
        if (out is Outcome.Ok && !out.value.signedIn && hasCredentials()) {
            // Cookie expired but the server still answers 200 with user:null.
            return if (reloginOnce()) getJson("/api/session", serializer<SessionInfo>())
            else Outcome.Err(MemdError.SignedOut)
        }
        return out
    }

    /** GET /rc/api/agents — HTML fallthrough (rc off) surfaces as NotJson. */
    suspend fun agents(): Outcome<AgentsResult> =
        getJson("/rc/api/agents", serializer<AgentsResponse>()).map { resp ->
            if (resp.view_host.isNotEmpty()) AgentsResult.ViewHostMode(resp.view_host)
            else AgentsResult.Machines(resp.agents)
        }

    /** GET <base><agent.url>api/tabs — agentUrl like "/rc/t/<64hex>/". */
    suspend fun tabs(agentUrl: String): Outcome<List<TabInfo>> {
        val path = if (agentUrl.endsWith("/")) "${agentUrl}api/tabs" else "$agentUrl/api/tabs"
        return getJson(path, ListSerializer(serializer<TabInfo>()))
    }

    /**
     * Force a re-login now (WebView got a 401 on the main frame). Returns true
     * when a fresh cookie was obtained.
     */
    suspend fun reloginNow(): Boolean = reloginOnce()

    // ── internals ──────────────────────────────────────────────────────────

    private suspend fun loginRaw(base: String, username: String, password: String): Outcome<Unit> {
        val body = WireJson.encodeToString(serializer<LoginRequest>(), LoginRequest(username, password))
        val request = Request.Builder()
            .url(Urls.join(base, "/api/auth/login"))
            .post(body.toRequestBody(JSON_TYPE))
            .build()
        return try {
            execute(request).use { resp ->
                when {
                    resp.isSuccessful -> Outcome.Ok(Unit)
                    resp.code == 401 -> Outcome.Err(MemdError.Http(401)) // bad creds
                    else -> Outcome.Err(MemdError.Http(resp.code))
                }
            }
        } catch (e: IOException) {
            Outcome.Err(MemdError.Network(e))
        }
    }

    /** One serialized login attempt with the stored credentials. */
    private suspend fun reloginOnce(): Boolean = loginMutex.withLock {
        val base = baseProvider() ?: return false
        val u = creds.get(KEY_USER) ?: return false
        val p = creds.get(KEY_PASS) ?: return false
        loginRaw(base, u, p) is Outcome.Ok
    }

    private suspend fun <T> getJson(
        path: String,
        deserializer: kotlinx.serialization.DeserializationStrategy<T>,
    ): Outcome<T> {
        val base = baseProvider() ?: return Outcome.Err(MemdError.NoServer)
        val url = Urls.join(base, path).toHttpUrlOrNull()
            ?: return Outcome.Err(MemdError.NoServer)
        val request = Request.Builder().url(url).get().build()

        var resp: Response = try {
            execute(request)
        } catch (e: IOException) {
            return Outcome.Err(MemdError.Network(e))
        }

        // The routine 24h-cookie-expiry path: one re-login, one retry.
        if (resp.code == 401) {
            resp.close()
            if (!hasCredentials() || !reloginOnce()) return Outcome.Err(MemdError.SignedOut)
            resp = try {
                execute(request)
            } catch (e: IOException) {
                return Outcome.Err(MemdError.Network(e))
            }
            if (resp.code == 401) {
                resp.close()
                return Outcome.Err(MemdError.SignedOut)
            }
        }

        resp.use { r ->
            if (!r.isSuccessful) return Outcome.Err(MemdError.Http(r.code))
            val bodyText = try {
                r.body?.string() ?: return Outcome.Err(MemdError.NotJson)
            } catch (e: IOException) {
                return Outcome.Err(MemdError.Network(e))
            }
            // Never parse blind: rc-off makes /rc/api/agents fall through to the
            // SPA and return HTML with 200. Check content type AND first byte.
            val contentType = r.header("Content-Type") ?: ""
            val firstChar = bodyText.trimStart().firstOrNull()
            val looksJson = firstChar == '{' || firstChar == '['
            if (!contentType.contains("json", ignoreCase = true) || !looksJson) {
                return Outcome.Err(MemdError.NotJson)
            }
            return try {
                Outcome.Ok(WireJson.decodeFromString(deserializer, bodyText))
            } catch (e: Exception) {
                Outcome.Err(MemdError.BadPayload(e))
            }
        }
    }

    private suspend fun execute(request: Request): Response =
        runInterruptible(Dispatchers.IO) { http.newCall(request).execute() }

    companion object {
        private val JSON_TYPE = "application/json; charset=utf-8".toMediaType()
        private const val KEY_USER = "creds.username"
        private const val KEY_PASS = "creds.password"
    }
}
