package com.debkosh.termulaa.core

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import java.time.Instant

class TimesTest {

    @Test
    fun `parses zulu timestamps`() {
        val i = Times.parseRfc3339("2026-08-30T10:15:30Z")
        assertEquals(Instant.parse("2026-08-30T10:15:30Z"), i)
    }

    @Test
    fun `parses offset timestamps`() {
        val i = Times.parseRfc3339("2026-08-30T15:45:30+05:30")
        assertEquals(Instant.parse("2026-08-30T10:15:30Z"), i)
    }

    @Test
    fun `parses fractional seconds`() {
        val i = Times.parseRfc3339("2026-08-30T10:15:30.123456789Z")
        assertEquals(Instant.parse("2026-08-30T10:15:30.123456789Z"), i)
    }

    @Test
    fun `garbage and blank parse to null`() {
        assertNull(Times.parseRfc3339("not-a-time"))
        assertNull(Times.parseRfc3339(""))
        assertNull(Times.parseRfc3339(null))
        assertNull(Times.parseRfc3339("2026-13-45T99:99:99Z"))
    }

    @Test
    fun `zero time is the never sentinel`() {
        assertTrue(Times.isNever(Times.parseRfc3339("0001-01-01T00:00:00Z")))
        assertTrue(Times.isNever(Times.parseRfc3339("1970-01-01T00:00:00Z")))
        assertFalse(Times.isNever(Times.parseRfc3339("2001-01-01T00:00:00Z")))
        assertTrue(Times.isNever(null))
    }

    @Test
    fun `parseOrNever collapses sentinel to null`() {
        assertNull(Times.parseOrNever("0001-01-01T00:00:00Z"))
        assertNull(Times.parseOrNever(null))
        assertEquals(
            Instant.parse("2026-08-30T10:15:30Z"),
            Times.parseOrNever("2026-08-30T10:15:30Z"),
        )
    }

    @Test
    fun `relative formatting ladder`() {
        val now = 1_000_000_000_000L
        assertEquals("just now", Times.relative(now - 5_000, now))
        assertEquals("45s ago", Times.relative(now - 45_000, now))
        assertEquals("3m ago", Times.relative(now - 3 * 60_000, now))
        assertEquals("2h ago", Times.relative(now - 2 * 3_600_000, now))
        assertEquals("5d ago", Times.relative(now - 5 * 86_400_000L, now))
        // future timestamps clamp to just now, never negative
        assertEquals("just now", Times.relative(now + 60_000, now))
    }
}
