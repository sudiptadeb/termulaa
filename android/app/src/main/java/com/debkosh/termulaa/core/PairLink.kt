package com.debkosh.termulaa.core

import java.net.URI
import java.net.URLDecoder

/**
 * Pure parsing + policy for the pairing deep link the dashboard renders:
 *
 *   termulaa://pair?server=<origin>&code=<code>
 *
 * Kept free of android.net.Uri so plain JVM tests can cover every branch.
 * Policy: a link for the configured server (or a fresh install) is safe to
 * auto-redeem; a link for a DIFFERENT server only prefills the Connect form —
 * the user must see the switch and tap Pair themselves, because pairing with
 * the new server clears the old sign-in.
 */
object PairLink {

    data class Parsed(val server: String, val code: String)

    sealed class Decision {
        /** No server configured yet, or the link matches it — redeem right away. */
        data class AutoPair(val server: String, val code: String) : Decision()
        /** Link is for a different server: prefill only, never auto-submit. */
        data class PrefillOnly(val server: String, val code: String, val message: String) : Decision()
        /** Not a usable pairing link — do nothing. */
        object Ignore : Decision()
    }

    /**
     * Extracts server+code from a candidate URI string. Returns null for
     * anything that is not a well-formed termulaa://pair link with both
     * parameters present. The server value may be percent-encoded or raw.
     */
    fun parse(uri: String?): Parsed? {
        if (uri.isNullOrBlank()) return null
        val u = try {
            URI(uri)
        } catch (_: Exception) {
            return null
        }
        if (!"termulaa".equals(u.scheme, ignoreCase = true)) return null
        val host = u.host ?: u.authority ?: return null
        if (!"pair".equals(host, ignoreCase = true)) return null
        val params = parseQuery(u.rawQuery ?: return null)
        val server = params["server"]?.trim().orEmpty()
        val code = params["code"]?.trim().orEmpty()
        if (server.isEmpty() || code.isEmpty()) return null
        return Parsed(server, code)
    }

    /**
     * Decides what a (possibly null) parsed link should do given the server
     * this phone is currently set up for (null = fresh install).
     */
    fun decide(parsed: Parsed?, configuredServer: String?): Decision {
        if (parsed == null) return Decision.Ignore
        val linkBase = (Urls.normalizeBase(parsed.server) as? Urls.BaseResult.Ok)?.base
            ?: return Decision.Ignore
        val configured = configuredServer
            ?.let { (Urls.normalizeBase(it) as? Urls.BaseResult.Ok)?.base }
        return if (configured == null || configured == linkBase) {
            Decision.AutoPair(linkBase, parsed.code)
        } else {
            Decision.PrefillOnly(
                server = linkBase,
                code = parsed.code,
                message = "This pairing link is for $linkBase, but this phone is set up for " +
                    "$configured. Tapping Pair switches servers and clears the current sign-in.",
            )
        }
    }

    private fun parseQuery(raw: String): Map<String, String> =
        raw.split('&').mapNotNull { part ->
            val i = part.indexOf('=')
            if (i <= 0) null else decode(part.take(i)) to decode(part.substring(i + 1))
        }.toMap()

    private fun decode(s: String): String = try {
        URLDecoder.decode(s, "UTF-8")
    } catch (_: Exception) {
        s
    }
}
