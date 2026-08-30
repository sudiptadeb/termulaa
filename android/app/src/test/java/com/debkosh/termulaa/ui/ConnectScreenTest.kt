package com.debkosh.termulaa.ui

import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.junit4.AndroidComposeTestRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performTextClearance
import androidx.compose.ui.test.performTextInput
import org.robolectric.RobolectricTestRunner
import org.junit.Assert.assertEquals
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith

/** Robolectric smoke tests: the Connect screen renders and submits. */
@RunWith(RobolectricTestRunner::class)
class ConnectScreenTest {

    @get:Rule
    val compose = createRoboComposeRule()

    @Test
    fun rendersDefaultServerAndFootnote() {
        compose.setContent {
            TermulaaTheme {
                ConnectScreen(busy = false, error = null, onConnect = { _, _, _ -> })
            }
        }
        compose.onNodeWithText("https://memd.debkosh.com").assertIsDisplayed()
        compose.onNodeWithText(
            "Your memd account — the same login as the web dashboard."
        ).assertIsDisplayed()
        compose.onNodeWithTag("connect").assertIsDisplayed()
    }

    @Test
    fun submitsEnteredValues() {
        var got: Triple<String, String, String>? = null
        compose.setContent {
            TermulaaTheme {
                ConnectScreen(busy = false, error = null) { url, user, pass ->
                    got = Triple(url, user, pass)
                }
            }
        }
        compose.onNodeWithTag("server").performTextClearance()
        compose.onNodeWithTag("server").performTextInput("https://memd.example.org")
        compose.onNodeWithTag("username").performTextInput("deb")
        compose.onNodeWithTag("password").performTextInput("hunter2")
        compose.onNodeWithTag("connect").performClick()

        assertEquals(
            Triple("https://memd.example.org", "deb", "hunter2"),
            got,
        )
    }

    @Test
    fun showsErrorLine() {
        compose.setContent {
            TermulaaTheme {
                ConnectScreen(busy = false, error = "Wrong username or password", onConnect = { _, _, _ -> })
            }
        }
        compose.onNodeWithTag("error").assertIsDisplayed()
        compose.onNodeWithText("Wrong username or password").assertIsDisplayed()
    }
}
