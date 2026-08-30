package com.debkosh.termulaa.ui

import android.annotation.SuppressLint
import android.content.Intent
import androidx.core.net.toUri
import android.view.ViewGroup
import android.view.WindowManager
import android.webkit.RenderProcessGoneDetail
import android.webkit.WebResourceRequest
import android.webkit.WebResourceResponse
import android.webkit.WebSettings
import android.webkit.WebView
import android.webkit.WebViewClient
import androidx.activity.compose.BackHandler
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.imePadding
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material.icons.automirrored.filled.OpenInNew
import androidx.compose.material.icons.filled.Brightness7
import androidx.compose.material.icons.filled.BrightnessLow
import androidx.compose.material.icons.filled.Refresh
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.Text
import androidx.compose.material3.TopAppBar
import androidx.compose.material3.TopAppBarDefaults
import androidx.compose.runtime.Composable
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.viewinterop.AndroidView
import androidx.core.graphics.toColorInt
import com.debkosh.termulaa.core.Urls
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.launch
import okhttp3.HttpUrl.Companion.toHttpUrlOrNull

/**
 * Full-screen WebView wrapping the termulaa web terminal. The page itself is
 * a complete touch-friendly terminal — this screen adds NOTHING inside it.
 *
 * Riskiest screen in the app (untestable without a device), so the rules are
 * deliberately few and defensive:
 *  - same-origin navigations stay in the WebView; anything else → browser
 *  - main-frame 401 → one re-login + cookie re-sync + one reload
 *  - onRenderProcessGone → recreate the WebView instead of crashing
 *  - back → WebView history while same-origin, else navigate up
 */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun TerminalScreen(
    label: String,
    terminalUrl: String,
    onBack: () -> Unit,
    /** Suspend: re-login and re-sync cookie; true when a retry makes sense. */
    onAuthFailed: suspend () -> Boolean,
) {
    val context = LocalContext.current
    var keepScreenOn by remember { mutableStateOf(false) }
    var webView by remember { mutableStateOf<WebView?>(null) }
    // Bumping this recreates the AndroidView factory (render process death).
    var webViewGeneration by remember { mutableIntStateOf(0) }
    var loading by remember { mutableStateOf(true) }

    val activity = context as? android.app.Activity
    val authScope = rememberCoroutineScope()

    DisposableEffect(keepScreenOn) {
        val window = activity?.window
        if (keepScreenOn) window?.addFlags(WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON)
        else window?.clearFlags(WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON)
        onDispose { window?.clearFlags(WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON) }
    }

    // Destroy the WebView with the screen; a leaked WebView keeps sockets open.
    DisposableEffect(Unit) {
        onDispose {
            try {
                webView?.apply {
                    stopLoading()
                    destroy()
                }
            } catch (_: Throwable) {
            }
            webView = null
        }
    }

    BackHandler {
        val wv = webView
        val current = wv?.url?.toHttpUrlOrNull()
        val base = terminalUrl.toHttpUrlOrNull()
        if (wv != null && wv.canGoBack() && Urls.sameOrigin(current, base)) {
            wv.goBack()
        } else {
            onBack()
        }
    }

    Column(Modifier.fillMaxSize().imePadding()) {
        TopAppBar(
            colors = TopAppBarDefaults.topAppBarColors(
                containerColor = Palette.Bg,
                titleContentColor = Palette.Text,
            ),
            title = {
                Text(
                    label.ifBlank { "(unnamed)" },
                    fontFamily = FontFamily.Monospace,
                    style = androidx.compose.material3.MaterialTheme.typography.titleSmall,
                )
            },
            navigationIcon = {
                IconButton(onClick = onBack) {
                    Icon(Icons.AutoMirrored.Filled.ArrowBack, "Back", tint = Palette.Dim)
                }
            },
            actions = {
                IconButton(onClick = { keepScreenOn = !keepScreenOn }) {
                    Icon(
                        if (keepScreenOn) Icons.Default.Brightness7 else Icons.Default.BrightnessLow,
                        contentDescription = "Keep screen on",
                        tint = if (keepScreenOn) Palette.Green else Palette.Dim,
                    )
                }
                IconButton(onClick = { webView?.reload() }) {
                    Icon(Icons.Default.Refresh, "Refresh", tint = Palette.Dim)
                }
                IconButton(onClick = {
                    try {
                        context.startActivity(Intent(Intent.ACTION_VIEW, terminalUrl.toUri()))
                    } catch (_: Exception) {
                        // No browser installed — nothing sensible to do.
                    }
                }) {
                    Icon(Icons.AutoMirrored.Filled.OpenInNew, "Open in browser", tint = Palette.Dim)
                }
            },
        )

        Box(Modifier.fillMaxSize()) {
            // key() forces a brand-new AndroidView (and factory run) when the
            // render process dies — AndroidView's factory otherwise runs once.
            androidx.compose.runtime.key(webViewGeneration) {
            AndroidView(
                modifier = Modifier.fillMaxSize(),
                factory = { ctx ->
                    // webViewGeneration read here so a render-process death
                    // (which nulls webView and bumps the counter) rebuilds.
                    createTerminalWebView(
                        ctx = ctx,
                        terminalUrl = terminalUrl,
                        onCreated = { webView = it },
                        onLoading = { loading = it },
                        onRenderGone = {
                            webView = null
                            webViewGeneration++
                        },
                        onAuthFailed = onAuthFailed,
                        authScope = authScope,
                        openExternal = { url ->
                            try {
                                context.startActivity(Intent(Intent.ACTION_VIEW, url.toUri()))
                            } catch (_: Exception) {
                            }
                        },
                    )
                },
                update = { /* state lives in the WebView itself */ },
            )
            }
            if (loading) {
                CircularProgressIndicator(
                    modifier = Modifier.align(Alignment.Center),
                    color = Palette.Green,
                )
            }
        }
    }
}

@SuppressLint("SetJavaScriptEnabled")
private fun createTerminalWebView(
    ctx: android.content.Context,
    terminalUrl: String,
    onCreated: (WebView) -> Unit,
    onLoading: (Boolean) -> Unit,
    onRenderGone: () -> Unit,
    onAuthFailed: suspend () -> Boolean,
    authScope: CoroutineScope,
    openExternal: (String) -> Unit,
): WebView {
    val baseUrl = terminalUrl.toHttpUrlOrNull()
    val wv = WebView(ctx)
    wv.layoutParams = ViewGroup.LayoutParams(
        ViewGroup.LayoutParams.MATCH_PARENT,
        ViewGroup.LayoutParams.MATCH_PARENT,
    )
    wv.setBackgroundColor("#0F1115".toColorInt())
    wv.settings.apply {
        // The termulaa UI is an xterm.js SPA: JS + DOM storage required.
        javaScriptEnabled = true
        domStorageEnabled = true
        // Locked down: no file/content access, no mixed content, no wide viewport.
        allowFileAccess = false
        allowContentAccess = false
        mixedContentMode = WebSettings.MIXED_CONTENT_NEVER_ALLOW
        useWideViewPort = false
        loadWithOverviewMode = false
        setSupportZoom(false)
        mediaPlaybackRequiresUserGesture = true
        cacheMode = WebSettings.LOAD_DEFAULT
    }

    wv.webViewClient = object : WebViewClient() {
        /** One auth-triggered reload per page load, to avoid 401 loops. */
        private var authRetried = false

        override fun shouldOverrideUrlLoading(view: WebView, request: WebResourceRequest): Boolean {
            val target = request.url?.toString()?.toHttpUrlOrNull()
            return if (Urls.sameOrigin(target, baseUrl)) {
                false // same origin: let the WebView navigate
            } else {
                request.url?.let { openExternal(it.toString()) }
                true
            }
        }

        override fun onPageStarted(view: WebView?, url: String?, favicon: android.graphics.Bitmap?) {
            onLoading(true)
        }

        override fun onPageFinished(view: WebView?, url: String?) {
            onLoading(false)
        }

        override fun onReceivedHttpError(
            view: WebView,
            request: WebResourceRequest,
            errorResponse: WebResourceResponse,
        ) {
            // Only the main frame, only 401, only once: the routine session
            // expiry inside a living WebView. Re-login via OkHttp (the client
            // mirrors the fresh cookie into the WebView jar), then reload.
            if (!request.isForMainFrame || errorResponse.statusCode != 401 || authRetried) return
            authRetried = true
            authScope.launch {
                if (onAuthFailed()) view.reload()
            }
        }

        override fun onRenderProcessGone(view: WebView, detail: RenderProcessGoneDetail): Boolean {
            // Detach + destroy the dead WebView and signal recreation.
            try {
                (view.parent as? ViewGroup)?.removeView(view)
                view.destroy()
            } catch (_: Throwable) {
            }
            onRenderGone()
            return true // handled — do NOT crash the app
        }
    }

    onCreated(wv)
    if (baseUrl != null) {
        wv.loadUrl(terminalUrl)
    }
    return wv
}
