package com.debkosh.termulaa.net

import com.debkosh.termulaa.core.Urls
import com.debkosh.termulaa.data.Agent
import com.debkosh.termulaa.data.AgentsResponse
import com.debkosh.termulaa.data.LoginRequest
import com.debkosh.termulaa.data.RedeemRequest
import com.debkosh.termulaa.data.RedeemResponse
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
    /**
     * 401 (or user:null) and no way to recover → show Connect. [notice] is a
     * human explanation when one exists (e.g. the phone was un-paired).
     */
    class SignedOut(val notice: String? = null) : MemdError("signed out")
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
 * and the transparent re-auth rule:
 *
 *   on any 401 (or /api/session with user:null), perform ONE re-auth attempt
 *   and retry the original request ONCE; if that fails → Outcome.Err(SignedOut).
 *
 * Re-auth strategy order: the paired app token (POST /api/app/session with
 * Authorization: Bearer) when one is stored — this covers OIDC/Google
 * accounts, which have no password — falling back to stored username/password
 * (POST /api/auth/login) only when no app token exists. A 401 on the bearer
 * call means the phone was un-paired from the dashboard: that is definitive,
 * no password fallback is attempted.
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

    /**
     * Human notice explaining the most recent auth loss (currently only "this
     * phone was un-paired"). The Connect screen shows it as a banner. Cleared
     * by any successful (re-)auth and by [clearAuth].
     */
    @Volatile var authNotice: String? = null
        private set

    // ── credentials ────────────────────────────────────────────────────────

    fun storeCredentials(username: String, password: String) {
        creds.put(KEY_USER, username)
        creds.put(KEY_PASS, password)
    }

    fun hasCredentials(): Boolean =
        creds.get(KEY_USER) != null && creds.get(KEY_PASS) != null

    fun hasAppToken(): Boolean = creds.get(KEY_TOKEN) != null

    private fun hasAnyAuth(): Boolean = hasAppToken() || hasCredentials()

    fun clearAuth() {
        creds.put(KEY_USER, null)
        creds.put(KEY_PASS, null)
        creds.put(KEY_TOKEN, null)
        authNotice = null
        cookieJar.clear()
    }

    // ── public API ─────────────────────────────────────────────────────────

    /** POST /api/auth/login with explicit credentials (password expander). */
    suspend fun login(username: String, password: String): Outcome<Unit> {
        val base = baseProvider() ?: return Outcome.Err(MemdError.NoServer)
        return loginRaw(base, username, password)
    }

    /**
     * POST /api/app/redeem — exchange a dashboard pairing code for a long-lived
     * app token (stored) plus a fresh session cookie (Set-Cookie → jar). No
     * auth required; the code is normalized (dashes/spaces stripped, uppercased)
     * so pasted or typed variants all work. 401 = invalid/expired code.
     */
    suspend fun redeem(code: String, label: String): Outcome<Unit> {
        val base = baseProvider() ?: return Outcome.Err(MemdError.NoServer)
        val payload = RedeemRequest(code = normalizeCode(code), label = label.take(64))
        val body = WireJson.encodeToString(serializer<RedeemRequest>(), payload)
        val request = Request.Builder()
            .url(Urls.join(base, "/api/app/redeem"))
            .post(body.toRequestBody(JSON_TYPE))
            .build()
        return try {
            execute(request).use { resp ->
                if (!resp.isSuccessful) return Outcome.Err(MemdError.Http(resp.code))
                val text = try {
                    resp.body?.string().orEmpty()
                } catch (e: IOException) {
                    return Outcome.Err(MemdError.Network(e))
                }
                val token = try {
                    WireJson.decodeFromString(serializer<RedeemResponse>(), text).token
                } catch (e: Exception) {
                    return Outcome.Err(MemdError.BadPayload(e))
                }
                if (token.isNullOrBlank()) {
                    return Outcome.Err(
                        MemdError.BadPayload(IllegalStateException("redeem response had no token"))
                    )
                }
                creds.put(KEY_TOKEN, token)
                authNotice = null
                Outcome.Ok(Unit)
            }
        } catch (e: IOException) {
            Outcome.Err(MemdError.Network(e))
        }
    }

    /**
     * POST /api/app/session with the stored bearer token — mints a fresh
     * memd_session cookie (arrives via Set-Cookie). A 401 means the token was
     * revoked (phone un-paired from the dashboard): SignedOut with the human
     * notice, and NO fallback to anything else.
     */
    suspend fun bearerSession(): Outcome<SessionInfo> {
        val base = baseProvider() ?: return Outcome.Err(MemdError.NoServer)
        val token = creds.get(KEY_TOKEN) ?: return Outcome.Err(MemdError.SignedOut())
        val request = Request.Builder()
            .url(Urls.join(base, "/api/app/session"))
            .post(ByteArray(0).toRequestBody(null))
            .header("Authorization", "Bearer $token")
            .build()
        return try {
            execute(request).use { resp ->
                when {
                    resp.code == 401 -> {
                        authNotice = UNPAIRED_NOTICE
                        Outcome.Err(MemdError.SignedOut(UNPAIRED_NOTICE))
                    }
                    !resp.isSuccessful -> Outcome.Err(MemdError.Http(resp.code))
                    else -> {
                        val text = try {
                            resp.body?.string().orEmpty()
                        } catch (e: IOException) {
                            return Outcome.Err(MemdError.Network(e))
                        }
                        try {
                            val info = WireJson.decodeFromString(serializer<SessionInfo>(), text)
                            authNotice = null
                            Outcome.Ok(info)
                        } catch (e: Exception) {
                            Outcome.Err(MemdError.BadPayload(e))
                        }
                    }
                }
            }
        } catch (e: IOException) {
            Outcome.Err(MemdError.Network(e))
        }
    }

    /**
     * DELETE /api/app/tokens/self with the bearer token — the sign-out
     * courtesy call so the dashboard's "paired phones" list stays honest.
     * Ok(Unit) when there is no token (nothing to revoke).
     */
    suspend fun revokeSelf(): Outcome<Unit> {
        val base = baseProvider() ?: return Outcome.Err(MemdError.NoServer)
        val token = creds.get(KEY_TOKEN) ?: return Outcome.Ok(Unit)
        val request = Request.Builder()
            .url(Urls.join(base, "/api/app/tokens/self"))
            .delete()
            .header("Authorization", "Bearer $token")
            .build()
        return try {
            execute(request).use { resp ->
                if (resp.isSuccessful) Outcome.Ok(Unit) else Outcome.Err(MemdError.Http(resp.code))
            }
        } catch (e: IOException) {
            Outcome.Err(MemdError.Network(e))
        }
    }

    /**
     * The sign-out path: best-effort token revocation, then unconditional
     * local wipe (token, credentials, cookie). Never throws — a dead server
     * must not block signing out.
     */
    suspend fun signOut() {
        try {
            revokeSelf()
        } catch (_: Exception) {
            // Best effort only; the dashboard can always revoke manually.
        }
        clearAuth()
    }

    /** GET /api/session (with transparent re-auth on user:null/401). */
    suspend fun session(): Outcome<SessionInfo> {
        val out = getJson("/api/session", serializer<SessionInfo>())
        if (out is Outcome.Ok && !out.value.signedIn && hasAnyAuth()) {
            // Cookie expired but the server still answers 200 with user:null.
            return if (reAuthOnce()) getJson("/api/session", serializer<SessionInfo>())
            else Outcome.Err(MemdError.SignedOut(authNotice))
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
     * Force a re-auth now (WebView got a 401 on the main frame). Returns true
     * when a fresh cookie was obtained.
     */
    suspend fun reloginNow(): Boolean = reAuthOnce()

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
                    resp.isSuccessful -> {
                        authNotice = null
                        Outcome.Ok(Unit)
                    }
                    resp.code == 401 -> Outcome.Err(MemdError.Http(401)) // bad creds
                    else -> Outcome.Err(MemdError.Http(resp.code))
                }
            }
        } catch (e: IOException) {
            Outcome.Err(MemdError.Network(e))
        }
    }

    /**
     * One serialized re-auth attempt: app token first (the only strategy that
     * works for OIDC accounts, and preferred when both exist), stored password
     * credentials only when no token is stored.
     */
    private suspend fun reAuthOnce(): Boolean = loginMutex.withLock {
        if (creds.get(KEY_TOKEN) != null) return@withLock bearerSession() is Outcome.Ok
        val base = baseProvider() ?: return@withLock false
        val u = creds.get(KEY_USER) ?: return@withLock false
        val p = creds.get(KEY_PASS) ?: return@withLock false
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

        // The routine 24h-cookie-expiry path: one re-auth, one retry.
        if (resp.code == 401) {
            resp.close()
            if (!hasAnyAuth() || !reAuthOnce()) return Outcome.Err(MemdError.SignedOut(authNotice))
            resp = try {
                execute(request)
            } catch (e: IOException) {
                return Outcome.Err(MemdError.Network(e))
            }
            if (resp.code == 401) {
                resp.close()
                return Outcome.Err(MemdError.SignedOut(authNotice))
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
        private const val KEY_TOKEN = "creds.apptoken"

        const val UNPAIRED_NOTICE = "This phone was un-paired — pair it again from the dashboard"

        /**
         * Pairing-code input is forgiving: dashes and any whitespace are
         * stripped, and the result is uppercased ("abc-def-ghj" == "ABCDEFGHJ").
         */
        fun normalizeCode(raw: String): String =
            raw.filterNot { it == '-' || it.isWhitespace() }.uppercase()
    }
}
