package com.debkosh.termulaa.core

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * The termulaa://pair deep-link parser and the auto-redeem-vs-prefill policy —
 * pure functions, so every branch (including malformed URIs and the
 * mismatched-server case) is covered on the JVM.
 */
class PairLinkTest {

    // ── parse ──────────────────────────────────────────────────────────────

    @Test
    fun `parses a link with a percent-encoded server`() {
        val p = PairLink.parse(
            "termulaa://pair?server=https%3A%2F%2Fmemd.example.org&code=ABC-DEF-GHJ"
        )
        assertEquals(PairLink.Parsed("https://memd.example.org", "ABC-DEF-GHJ"), p)
    }

    @Test
    fun `parses a link with a raw unencoded server`() {
        val p = PairLink.parse("termulaa://pair?server=https://memd.example.org&code=abcdefghj")
        assertEquals(PairLink.Parsed("https://memd.example.org", "abcdefghj"), p)
    }

    @Test
    fun `parse keeps the code exactly as given - normalization happens later`() {
        val p = PairLink.parse("termulaa://pair?server=https://x.example&code=abc-DEF-ghj")
        assertEquals("abc-DEF-ghj", p?.code)
    }

    @Test
    fun `malformed or foreign uris parse to null`() {
        val bad = listOf(
            null,
            "",
            "   ",
            "not a uri at all",
            "termulaa://pair",                                         // no query
            "termulaa://pair?code=ABCDEFGHJ",                          // missing server
            "termulaa://pair?server=https://x.example",                // missing code
            "termulaa://pair?server=&code=ABCDEFGHJ",                  // empty server
            "termulaa://other?server=https://x.example&code=ABCDEFGHJ", // wrong host
            "https://pair?server=https://x.example&code=ABCDEFGHJ",    // wrong scheme
            "termulaa://pair?%zz&code=ABCDEFGHJ",                      // broken escaping
        )
        for (uri in bad) {
            assertNull("expected null for: $uri", PairLink.parse(uri))
        }
    }

    // ── decide ─────────────────────────────────────────────────────────────

    @Test
    fun `no configured server means auto-pair with the normalized base`() {
        val d = PairLink.decide(
            PairLink.Parsed("https://Memd.Example.Org/", "ABC-DEF-GHJ"),
            configuredServer = null,
        )
        assertEquals(
            PairLink.Decision.AutoPair("https://memd.example.org", "ABC-DEF-GHJ"),
            d,
        )
    }

    @Test
    fun `same server (up to normalization) means auto-pair`() {
        val d = PairLink.decide(
            PairLink.Parsed("https://memd.example.org/", "ABCDEFGHJ"),
            configuredServer = "https://memd.example.org",
        )
        assertTrue(d is PairLink.Decision.AutoPair)
    }

    @Test
    fun `a different server only prefills and states the switch plainly`() {
        val d = PairLink.decide(
            PairLink.Parsed("https://new.example.org", "ABCDEFGHJ"),
            configuredServer = "https://old.example.org",
        )
        val prefill = d as PairLink.Decision.PrefillOnly
        assertEquals("https://new.example.org", prefill.server)
        assertEquals("ABCDEFGHJ", prefill.code)
        assertTrue(prefill.message.contains("https://new.example.org"))
        assertTrue(prefill.message.contains("https://old.example.org"))
        assertTrue(prefill.message.contains("clears the current sign-in"))
    }

    @Test
    fun `null parse or a link server that fails normalization is ignored`() {
        assertEquals(PairLink.Decision.Ignore, PairLink.decide(null, "https://x.example"))
        // http to a non-loopback host violates the app's https rule → Ignore.
        val d = PairLink.decide(
            PairLink.Parsed("http://memd.example.org", "ABCDEFGHJ"),
            configuredServer = null,
        )
        assertEquals(PairLink.Decision.Ignore, d)
    }
}
