package com.debkosh.termulaa.ui

import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performScrollTo
import androidx.compose.ui.test.performTextClearance
import androidx.compose.ui.test.performTextInput
import org.robolectric.RobolectricTestRunner
import org.junit.Assert.assertEquals
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith

/**
 * Robolectric tests for the code-first Connect screen: primary pairing form,
 * the collapsed password expander, deep-link prefill, and the notice banner.
 */
@RunWith(RobolectricTestRunner::class)
class ConnectScreenTest {

    @get:Rule
    val compose = createRoboComposeRule()

    private fun setScreen(
        error: String? = null,
        notice: String? = null,
        initialServerUrl: String = "https://memd.debkosh.com",
        initialCode: String = "",
        onPair: (String, String) -> Unit = { _, _ -> },
        onPasswordSignIn: (String, String, String) -> Unit = { _, _, _ -> },
    ) {
        compose.setContent {
            TermulaaTheme {
                ConnectScreen(
                    busy = false,
                    error = error,
                    notice = notice,
                    initialServerUrl = initialServerUrl,
                    initialCode = initialCode,
                    onPair = onPair,
                    onPasswordSignIn = onPasswordSignIn,
                )
            }
        }
    }

    @Test
    fun codeFirstUi_showsPairingFormAndHidesPasswordForm() {
        setScreen()
        compose.onNodeWithText("https://memd.debkosh.com").assertIsDisplayed()
        compose.onNodeWithTag("code").assertIsDisplayed()
        compose.onNodeWithText(
            "Get a code from the termulaa section of your dashboard — " +
                "works with Google/SSO sign-in"
        ).assertIsDisplayed()
        compose.onNodeWithTag("pair").assertIsDisplayed()
        compose.onNodeWithTag("passwordToggle").assertIsDisplayed()
        // Collapsed by default: no username/password fields, no Connect button.
        compose.onNodeWithTag("username").assertDoesNotExist()
        compose.onNodeWithTag("password").assertDoesNotExist()
        compose.onNodeWithTag("connect").assertDoesNotExist()
    }

    @Test
    fun pairSubmitsServerAndCodeAsTyped() {
        var got: Pair<String, String>? = null
        setScreen(onPair = { url, code -> got = url to code })
        compose.onNodeWithTag("server").performTextClearance()
        compose.onNodeWithTag("server").performTextInput("https://memd.example.org")
        // Forgiving input: lowercase and dashes are accepted as typed;
        // normalization happens in the client.
        compose.onNodeWithTag("code").performTextInput("abc-def-ghj")
        compose.onNodeWithTag("pair").performClick()
        assertEquals("https://memd.example.org" to "abc-def-ghj", got)
    }

    @Test
    fun passwordExpanderRevealsFormAndSubmits() {
        var got: Triple<String, String, String>? = null
        setScreen(onPasswordSignIn = { url, user, pass -> got = Triple(url, user, pass) })
        compose.onNodeWithText("Sign in with password instead").assertIsDisplayed()
        compose.onNodeWithTag("passwordToggle").performClick()
        compose.onNodeWithTag("username").performScrollTo().performTextInput("deb")
        compose.onNodeWithTag("password").performScrollTo().performTextInput("hunter2")
        compose.onNodeWithTag("connect").performScrollTo().performClick()
        assertEquals(Triple("https://memd.debkosh.com", "deb", "hunter2"), got)
    }

    @Test
    fun deepLinkPrefillRendersServerAndCode() {
        setScreen(
            initialServerUrl = "https://other.example.org",
            initialCode = "ABC-DEF-GHJ",
        )
        compose.onNodeWithText("https://other.example.org").assertIsDisplayed()
        compose.onNodeWithText("ABC-DEF-GHJ").assertIsDisplayed()
    }

    @Test
    fun showsNoticeBanner() {
        setScreen(notice = "This phone was un-paired — pair it again from the dashboard")
        compose.onNodeWithTag("notice").assertIsDisplayed()
        compose.onNodeWithText(
            "This phone was un-paired — pair it again from the dashboard"
        ).assertIsDisplayed()
    }

    @Test
    fun showsErrorLine() {
        setScreen(error = "That pairing code is invalid or expired — get a fresh one from your dashboard")
        compose.onNodeWithTag("error").assertIsDisplayed()
        compose.onNodeWithText(
            "That pairing code is invalid or expired — get a fresh one from your dashboard"
        ).assertIsDisplayed()
    }
}
