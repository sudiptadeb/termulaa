package com.debkosh.termulaa.core

import okhttp3.HttpUrl
import okhttp3.HttpUrl.Companion.toHttpUrlOrNull

/**
 * All URL policy in one pure object: base normalization (with the
 * https-required rule), joining server-relative paths, and same-origin checks
 * used by both the client and the WebView navigation gate.
 */
object Urls {

    sealed class BaseResult {
        /** Canonical base: scheme://host[:port][/path-prefix], NO trailing slash. */
        data class Ok(val base: String) : BaseResult()
        data class Invalid(val reason: String) : BaseResult()
    }

    /**
     * Normalizes a user-entered server URL.
     *  - whitespace trimmed; bare hosts get https:// prepended
     *  - https required, except http for literal IPs / localhost (dev servers)
     *  - trailing slashes stripped; a path prefix (reverse-proxy setup) is kept
     *  - query/fragment rejected
     */
    fun normalizeBase(input: String): BaseResult {
        var s = input.trim()
        if (s.isEmpty()) return BaseResult.Invalid("Enter a server URL")
        if (!s.contains("://")) s = "https://$s"
        val url = s.toHttpUrlOrNull()
            ?: return BaseResult.Invalid("Not a valid URL")
        if (url.scheme == "http" && !isLoopbackOrLiteralIp(url.host)) {
            return BaseResult.Invalid("https is required (http only for IPs/localhost)")
        }
        if (url.query != null || url.fragment != null) {
            return BaseResult.Invalid("Server URL must not contain ?query or #fragment")
        }
        if (url.username.isNotEmpty() || url.password.isNotEmpty()) {
            return BaseResult.Invalid("Server URL must not embed credentials")
        }
        // Rebuild canonical form; drop empty path segments (trailing "/", "//").
        val cleanSegments = url.pathSegments.filter { it.isNotEmpty() }
        val sb = StringBuilder("${url.scheme}://${url.host}")
        if (url.port != HttpUrl.defaultPort(url.scheme)) sb.append(":${url.port}")
        for (seg in cleanSegments) sb.append("/").append(seg)
        return BaseResult.Ok(sb.toString())
    }

    fun isLoopbackOrLiteralIp(host: String): Boolean {
        if (host.equals("localhost", ignoreCase = true)) return true
        // IPv6 literal (OkHttp strips the brackets from host)
        if (host.contains(':')) return true
        // IPv4 literal
        return host.matches(Regex("""\d{1,3}(\.\d{1,3}){3}"""))
    }

    /**
     * Joins a normalized base with a server-relative path ("/rc/api/agents",
     * agent.url like "/rc/t/<64hex>/"). Base never has a trailing slash, so a
     * plain concat is correct and keeps any reverse-proxy path prefix.
     */
    fun join(base: String, path: String): String {
        val p = if (path.startsWith("/")) path else "/$path"
        return base.trimEnd('/') + p
    }

    /** scheme + host + port equality — the WebView escape-hatch gate. */
    fun sameOrigin(a: HttpUrl?, b: HttpUrl?): Boolean {
        if (a == null || b == null) return false
        return a.scheme == b.scheme && a.host == b.host && a.port == b.port
    }
}
