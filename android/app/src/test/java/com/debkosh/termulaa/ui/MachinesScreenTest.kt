package com.debkosh.termulaa.ui

import androidx.compose.ui.test.assertCountEquals
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.junit4.AndroidComposeTestRule
import androidx.compose.ui.test.onAllNodesWithTag
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import org.robolectric.RobolectricTestRunner
import com.debkosh.termulaa.data.MachineRow
import org.junit.Assert.assertEquals
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith

/** Robolectric smoke tests: Machines screen renders fake state correctly. */
@RunWith(RobolectricTestRunner::class)
class MachinesScreenTest {

    @get:Rule
    val compose = createRoboComposeRule()

    private val now = 1_756_500_000_000L

    private fun onlineRow(id: String, label: String, unseen: Boolean = false) = MachineRow(
        id = id, label = label, online = true, agentUrl = "/rc/t/${"a".repeat(64)}/",
        tabCount = 3, lastOutput = now - 3 * 60_000, outputUnknown = false,
        unseen = unseen, offlineSince = null, notifyEnabled = true,
    )

    private fun offlineRow(id: String, label: String) = MachineRow(
        id = id, label = label, online = false, agentUrl = null,
        tabCount = null, lastOutput = null, outputUnknown = false,
        unseen = false, offlineSince = now - 2 * 3_600_000, notifyEnabled = true,
    )

    @Test
    fun rendersOnlineOfflineAndUnseenCards() {
        val state = MachinesUiState(
            rows = listOf(
                onlineRow("aa11bb22", "buildbox", unseen = true),
                onlineRow("cc33dd44", "quietbox"),
                offlineRow("ee55ff66", "laptop"),
            ),
            nowMillis = now,
        )
        compose.setContent { TermulaaTheme { MachinesScreen(state = state) } }

        compose.onNodeWithText("buildbox").assertIsDisplayed()
        compose.onNodeWithText("quietbox").assertIsDisplayed()
        compose.onNodeWithText("laptop").assertIsDisplayed()
        // monospace 8-hex ids visible
        compose.onNodeWithText("aa11bb22").assertIsDisplayed()
        // status dots: two online, one offline
        compose.onAllNodesWithTag("dot-online", useUnmergedTree = true).assertCountEquals(2)
        compose.onAllNodesWithTag("dot-offline", useUnmergedTree = true).assertCountEquals(1)
        // amber unseen pill only on the unseen machine
        compose.onNodeWithTag("unseen-aa11bb22", useUnmergedTree = true).assertIsDisplayed()
        compose.onAllNodesWithTag("unseen-cc33dd44", useUnmergedTree = true).assertCountEquals(0)
        // meta lines
        compose.onAllNodesWithText("3 tabs · output 3m ago").assertCountEquals(2)
        compose.onNodeWithText("offline since 2h ago").assertIsDisplayed()
    }

    @Test
    fun unnamedMachineShowsPlaceholder() {
        val state = MachinesUiState(rows = listOf(onlineRow("aa11bb22", "")), nowMillis = now)
        compose.setContent { TermulaaTheme { MachinesScreen(state = state) } }
        compose.onNodeWithText("(unnamed)").assertIsDisplayed()
    }

    @Test
    fun onlineCardTapOpensAndOfflineCardTapDoesNot() {
        var opened: String? = null
        var offlineTapped: String? = null
        val state = MachinesUiState(
            rows = listOf(onlineRow("aa11bb22", "buildbox"), offlineRow("ee55ff66", "laptop")),
            nowMillis = now,
        )
        compose.setContent {
            TermulaaTheme {
                MachinesScreen(
                    state = state,
                    onOpenMachine = { opened = it.id },
                    onOfflineTap = { offlineTapped = it.id },
                )
            }
        }
        compose.onNodeWithTag("machine-aa11bb22").performClick()
        assertEquals("aa11bb22", opened)
        compose.onNodeWithTag("machine-ee55ff66").performClick()
        assertEquals("ee55ff66", offlineTapped)
        assertEquals("aa11bb22", opened) // unchanged
    }

    @Test
    fun emptyStateShows() {
        compose.setContent {
            TermulaaTheme { MachinesScreen(state = MachinesUiState(rows = emptyList())) }
        }
        compose.onNodeWithText("No machines yet").assertIsDisplayed()
        compose.onNodeWithText("Open dashboard").assertIsDisplayed()
    }

    @Test
    fun bannersRender() {
        compose.setContent {
            TermulaaTheme {
                MachinesScreen(
                    state = MachinesUiState(banner = MachinesBanner.RcDisabled),
                )
            }
        }
        compose.onNodeWithText("Remote terminals are disabled on this server").assertIsDisplayed()
    }

    @Test
    fun activityUnavailableMetaForOlderAgents() {
        val row = onlineRow("aa11bb22", "old-agent").copy(outputUnknown = true, lastOutput = null)
        compose.setContent {
            TermulaaTheme { MachinesScreen(state = MachinesUiState(rows = listOf(row), nowMillis = now)) }
        }
        compose.onNodeWithText("3 tabs · activity unavailable — update termulaa on this machine")
            .assertIsDisplayed()
    }
}
