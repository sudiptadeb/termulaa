package com.debkosh.termulaa.core

import okhttp3.HttpUrl.Companion.toHttpUrl
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class UrlsTest {

    private fun ok(input: String): String =
        (Urls.normalizeBase(input) as Urls.BaseResult.Ok).base

    private fun invalid(input: String): Boolean =
        Urls.normalizeBase(input) is Urls.BaseResult.Invalid

    @Test
    fun `https URLs normalize with trailing slash stripped`() {
        assertEquals("https://memd.debkosh.com", ok("https://memd.debkosh.com"))
        assertEquals("https://memd.debkosh.com", ok("https://memd.debkosh.com/"))
        assertEquals("https://memd.debkosh.com", ok("  https://memd.debkosh.com//  "))
    }

    @Test
    fun `path prefix is preserved without trailing slash`() {
        assertEquals("https://example.com/memd", ok("https://example.com/memd/"))
        assertEquals("https://example.com/a/b", ok("https://example.com/a/b"))
    }

    @Test
    fun `bare host gets https`() {
        assertEquals("https://memd.debkosh.com", ok("memd.debkosh.com"))
    }

    @Test
    fun `non-default port kept`() {
        assertEquals("https://example.com:8443", ok("https://example.com:8443/"))
        assertEquals("http://127.0.0.1:8080", ok("http://127.0.0.1:8080"))
    }

    @Test
    fun `http requires literal IP or localhost`() {
        assertTrue(invalid("http://memd.debkosh.com"))
        assertEquals("http://127.0.0.1", ok("http://127.0.0.1"))
        assertEquals("http://localhost:3000", ok("http://localhost:3000"))
        assertEquals("http://10.0.2.2:8443", ok("http://10.0.2.2:8443"))
    }

    @Test
    fun `garbage rejected`() {
        assertTrue(invalid(""))
        assertTrue(invalid("   "))
        assertTrue(invalid("ftp://example.com"))
        assertTrue(invalid("https://exa mple.com"))
    }

    @Test
    fun `query fragment and creds rejected`() {
        assertTrue(invalid("https://example.com/?x=1"))
        assertTrue(invalid("https://example.com/#/termulaa"))
        assertTrue(invalid("https://user:pw@example.com"))
    }

    @Test
    fun `join concatenates server-relative paths`() {
        assertEquals(
            "https://memd.debkosh.com/rc/api/agents",
            Urls.join("https://memd.debkosh.com", "/rc/api/agents"),
        )
        // base with reverse-proxy prefix keeps the prefix
        assertEquals(
            "https://example.com/memd/rc/t/abc/",
            Urls.join("https://example.com/memd", "/rc/t/abc/"),
        )
        // defensive: base with an accidental trailing slash still joins clean
        assertEquals(
            "https://x.test/rc/api/agents",
            Urls.join("https://x.test/", "/rc/api/agents"),
        )
    }

    @Test
    fun `sameOrigin compares scheme host port`() {
        val a = "https://memd.debkosh.com/rc/t/abc/".toHttpUrl()
        val b = "https://memd.debkosh.com/other".toHttpUrl()
        val c = "http://memd.debkosh.com/rc/t/abc/".toHttpUrl()
        val d = "https://evil.test/rc/t/abc/".toHttpUrl()
        val e = "https://memd.debkosh.com:8443/".toHttpUrl()
        assertTrue(Urls.sameOrigin(a, b))
        assertFalse(Urls.sameOrigin(a, c))
        assertFalse(Urls.sameOrigin(a, d))
        assertFalse(Urls.sameOrigin(a, e))
        assertFalse(Urls.sameOrigin(a, null))
        assertFalse(Urls.sameOrigin(null, null))
    }
}
