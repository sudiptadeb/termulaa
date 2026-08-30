package com.debkosh.termulaa.core

import java.time.Instant
import java.time.OffsetDateTime
import java.time.ZoneOffset

/**
 * RFC3339 parsing + relative-time formatting, kept pure so it is trivially
 * unit-testable. The server sends "0001-01-01T00:00:00Z" (or other pre-2000
 * sentinels) to mean "never".
 */
object Times {

    /** Parses an RFC3339 timestamp; null on absent/blank/garbage input. */
    fun parseRfc3339(s: String?): Instant? {
        if (s.isNullOrBlank()) return null
        return try {
            OffsetDateTime.parse(s).toInstant()
        } catch (_: Exception) {
            try {
                Instant.parse(s)
            } catch (_: Exception) {
                null
            }
        }
    }

    /** Any timestamp before year 2000 is the server's "never" sentinel. */
    fun isNever(instant: Instant?): Boolean {
        if (instant == null) return true
        return instant.atOffset(ZoneOffset.UTC).year < 2000
    }

    /**
     * Parses and collapses the "never" sentinel to null, so callers only ever
     * deal with (real Instant | null).
     */
    fun parseOrNever(s: String?): Instant? {
        val i = parseRfc3339(s) ?: return null
        return if (isNever(i)) null else i
    }

    /** "just now", "45s ago", "3m ago", "2h ago", "5d ago". */
    fun relative(epochMillis: Long, nowMillis: Long): String {
        val delta = (nowMillis - epochMillis).coerceAtLeast(0) / 1000
        return when {
            delta < 10 -> "just now"
            delta < 60 -> "${delta}s ago"
            delta < 3600 -> "${delta / 60}m ago"
            delta < 86_400 -> "${delta / 3600}h ago"
            delta < 86_400L * 30 -> "${delta / 86_400}d ago"
            else -> "${delta / (86_400L * 30)}mo ago"
        }
    }
}
