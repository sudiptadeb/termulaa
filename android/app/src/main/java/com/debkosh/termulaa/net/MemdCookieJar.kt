package com.debkosh.termulaa.net

import okhttp3.Cookie
import okhttp3.CookieJar
import okhttp3.HttpUrl

/**
 * CookieJar holding exactly one cookie: memd_session for the configured base
 * host. Persisted (value + expiry) through [SecretStore]; every change is
 * pushed to [onCookieChanged] so the production graph can mirror it into
 * android.webkit.CookieManager for the WebView (tests just record it).
 *
 * Deliberately minimal: the memd server contract has one session cookie, so a
 * general multi-cookie jar would only add surface for bugs.
 */
class MemdCookieJar(
    private val store: SecretStore,
    /** (url, cookieHeaderValue or null-when-cleared) */
    private val onCookieChanged: (HttpUrl, String?) -> Unit = { _, _ -> },
) : CookieJar {

    override fun saveFromResponse(url: HttpUrl, cookies: List<Cookie>) {
        val session = cookies.lastOrNull { it.name == COOKIE_NAME } ?: return
        store.put(KEY_VALUE, session.value)
        store.put(KEY_EXPIRES, session.expiresAt.toString())
        store.put(KEY_HOST, url.host)
        onCookieChanged(url, "$COOKIE_NAME=${session.value}")
    }

    override fun loadForRequest(url: HttpUrl): List<Cookie> {
        val value = store.get(KEY_VALUE) ?: return emptyList()
        val host = store.get(KEY_HOST) ?: return emptyList()
        if (!url.host.equals(host, ignoreCase = true)) return emptyList()
        val expires = store.get(KEY_EXPIRES)?.toLongOrNull() ?: 0L
        if (expires in 1 until System.currentTimeMillis()) {
            clear()
            return emptyList()
        }
        val builder = Cookie.Builder()
            .name(COOKIE_NAME)
            .value(value)
            .domain(host)
            .path("/")
        if (url.isHttps) builder.secure()
        return listOf(builder.build())
    }

    fun currentCookieHeader(): String? =
        store.get(KEY_VALUE)?.let { "$COOKIE_NAME=$it" }

    fun clear() {
        store.put(KEY_VALUE, null)
        store.put(KEY_EXPIRES, null)
        store.put(KEY_HOST, null)
    }

    companion object {
        const val COOKIE_NAME = "memd_session"
        private const val KEY_VALUE = "cookie.value"
        private const val KEY_EXPIRES = "cookie.expires"
        private const val KEY_HOST = "cookie.host"
    }
}
