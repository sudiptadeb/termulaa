package com.debkosh.termulaa.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.MoreVert
import androidx.compose.material3.Button
import androidx.compose.material3.Card
import androidx.compose.material3.CardDefaults
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Scaffold
import androidx.compose.material3.SnackbarHost
import androidx.compose.material3.SnackbarHostState
import androidx.compose.material3.Switch
import androidx.compose.material3.SwitchDefaults
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.TopAppBar
import androidx.compose.material3.TopAppBarDefaults
import androidx.compose.material3.pulltorefresh.PullToRefreshBox
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.unit.dp
import com.debkosh.termulaa.core.Times
import com.debkosh.termulaa.data.MachineRow

sealed class MachinesBanner {
    object SignedOut : MachinesBanner()
    object RcDisabled : MachinesBanner()
    data class ViewHost(val host: String) : MachinesBanner()
    /** Network error — the stale remembered list is shown greyed underneath. */
    data class NetworkError(val lastUpdatedMillis: Long?) : MachinesBanner()
}

data class MachinesUiState(
    val loading: Boolean = false,
    val refreshing: Boolean = false,
    val rows: List<MachineRow> = emptyList(),
    val banner: MachinesBanner? = null,
    val watchOn: Boolean = false,
    val nowMillis: Long = System.currentTimeMillis(),
)

/** Stateless Machines screen; every effect is a hoisted callback. */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun MachinesScreen(
    state: MachinesUiState,
    snackbarHostState: SnackbarHostState = remember { SnackbarHostState() },
    onRefresh: () -> Unit = {},
    onOpenMachine: (MachineRow) -> Unit = {},
    onOfflineTap: (MachineRow) -> Unit = {},
    onMarkSeen: (String) -> Unit = {},
    onSetMuted: (String, Boolean) -> Unit = { _, _ -> },
    onForget: (String) -> Unit = {},
    onWatchToggle: (Boolean) -> Unit = {},
    onSettings: () -> Unit = {},
    onSignOut: () -> Unit = {},
    onGoConnect: () -> Unit = {},
    onOpenDashboard: () -> Unit = {},
) {
    var menuOpen by remember { mutableStateOf(false) }

    Scaffold(
        containerColor = Palette.Bg,
        snackbarHost = { SnackbarHost(snackbarHostState) },
        topBar = {
            TopAppBar(
                colors = TopAppBarDefaults.topAppBarColors(
                    containerColor = Palette.Bg,
                    titleContentColor = Palette.Text,
                ),
                title = {
                    Text("termulaa", fontFamily = FontFamily.Monospace, color = Palette.Green)
                },
                actions = {
                    Text("watch", color = Palette.Dim, style = MaterialTheme.typography.labelSmall)
                    Switch(
                        checked = state.watchOn,
                        onCheckedChange = onWatchToggle,
                        colors = SwitchDefaults.colors(checkedTrackColor = Palette.Green),
                        modifier = Modifier.padding(horizontal = 8.dp).testTag("watch-toggle"),
                    )
                    IconButton(onClick = { menuOpen = true }, modifier = Modifier.testTag("overflow")) {
                        Icon(Icons.Default.MoreVert, contentDescription = "Menu", tint = Palette.Dim)
                    }
                    DropdownMenu(expanded = menuOpen, onDismissRequest = { menuOpen = false }) {
                        DropdownMenuItem(
                            text = { Text("Settings") },
                            onClick = { menuOpen = false; onSettings() },
                        )
                        DropdownMenuItem(
                            text = { Text("Sign out") },
                            onClick = { menuOpen = false; onSignOut() },
                        )
                    }
                },
            )
        },
    ) { padding ->
        PullToRefreshBox(
            isRefreshing = state.refreshing,
            onRefresh = onRefresh,
            modifier = Modifier.padding(padding).fillMaxSize(),
        ) {
            LazyColumn(
                modifier = Modifier.fillMaxSize(),
                verticalArrangement = Arrangement.spacedBy(10.dp),
                contentPadding = androidx.compose.foundation.layout.PaddingValues(16.dp),
            ) {
                state.banner?.let { banner ->
                    item(key = "banner") { BannerCard(banner, onGoConnect) }
                }
                if (state.rows.isEmpty() && state.banner == null && !state.loading) {
                    item(key = "empty") { EmptyState(onOpenDashboard) }
                }
                items(state.rows, key = { it.id }) { row ->
                    MachineCard(
                        row = row,
                        nowMillis = state.nowMillis,
                        greyed = state.banner is MachinesBanner.NetworkError,
                        onTap = { if (row.online) onOpenMachine(row) else onOfflineTap(row) },
                        onMarkSeen = { onMarkSeen(row.id) },
                        onSetMuted = { onSetMuted(row.id, it) },
                        onForget = { onForget(row.id) },
                    )
                }
            }
        }
    }
}

@Composable
private fun BannerCard(banner: MachinesBanner, onGoConnect: () -> Unit) {
    Card(
        modifier = Modifier.fillMaxWidth().testTag("banner"),
        colors = CardDefaults.cardColors(containerColor = Palette.Surface),
        shape = RoundedCornerShape(10.dp),
    ) {
        Column(Modifier.padding(14.dp)) {
            when (banner) {
                MachinesBanner.SignedOut -> {
                    Text("Signed out", color = Palette.Red)
                    Text(
                        "Your session ended and sign-in failed.",
                        color = Palette.Dim,
                        style = MaterialTheme.typography.bodySmall,
                    )
                    TextButton(onClick = onGoConnect) { Text("Connect", color = Palette.Green) }
                }
                MachinesBanner.RcDisabled -> {
                    Text("Remote terminals are disabled on this server", color = Palette.Amber)
                    Text(
                        "Enable the termulaa feature on the memd server to use this app.",
                        color = Palette.Dim,
                        style = MaterialTheme.typography.bodySmall,
                    )
                }
                is MachinesBanner.ViewHost -> {
                    Text("Dedicated view host", color = Palette.Amber)
                    Text(
                        "This server uses a dedicated view host (${banner.host}); " +
                            "not yet supported by the app. Use the browser instead.",
                        color = Palette.Dim,
                        style = MaterialTheme.typography.bodySmall,
                    )
                }
                is MachinesBanner.NetworkError -> {
                    Text("Can't reach the server", color = Palette.Red)
                    val updated = banner.lastUpdatedMillis?.let {
                        "last updated ${Times.relative(it, System.currentTimeMillis())}"
                    } ?: "showing remembered machines"
                    Text(updated, color = Palette.Dim, style = MaterialTheme.typography.bodySmall)
                }
            }
        }
    }
}

@Composable
private fun EmptyState(onOpenDashboard: () -> Unit) {
    Column(
        modifier = Modifier.fillMaxWidth().padding(vertical = 64.dp).testTag("empty"),
        horizontalAlignment = Alignment.CenterHorizontally,
    ) {
        Text(">_", fontFamily = FontFamily.Monospace, color = Palette.Border,
            style = MaterialTheme.typography.headlineLarge)
        Spacer(Modifier.height(12.dp))
        Text("No machines yet", color = Palette.Text)
        Text(
            "Set one up from the memd dashboard",
            color = Palette.Dim,
            style = MaterialTheme.typography.bodySmall,
        )
        Spacer(Modifier.height(16.dp))
        Button(onClick = onOpenDashboard) { Text("Open dashboard") }
    }
}

@Composable
private fun MachineCard(
    row: MachineRow,
    nowMillis: Long,
    greyed: Boolean,
    onTap: () -> Unit,
    onMarkSeen: () -> Unit,
    onSetMuted: (Boolean) -> Unit,
    onForget: () -> Unit,
) {
    var menuOpen by remember { mutableStateOf(false) }
    val dim = if (greyed) 0.55f else 1f

    Card(
        modifier = Modifier
            .fillMaxWidth()
            .clickable(onClick = onTap)
            .testTag("machine-${row.id}"),
        colors = CardDefaults.cardColors(containerColor = Palette.Surface.copy(alpha = dim)),
        shape = RoundedCornerShape(10.dp),
    ) {
        Row(
            modifier = Modifier.padding(horizontal = 14.dp, vertical = 12.dp),
            verticalAlignment = Alignment.CenterVertically,
        ) {
            Box(
                modifier = Modifier
                    .size(10.dp)
                    .background(
                        color = if (row.online) Palette.Green else Palette.Red,
                        shape = CircleShape,
                    )
                    .testTag(if (row.online) "dot-online" else "dot-offline"),
            )
            Spacer(Modifier.width(12.dp))
            Column(Modifier.weight(1f)) {
                Row(verticalAlignment = Alignment.CenterVertically) {
                    Text(
                        text = row.label.ifBlank { "(unnamed)" },
                        color = Palette.Text.copy(alpha = dim),
                        style = MaterialTheme.typography.titleSmall,
                    )
                    Spacer(Modifier.width(8.dp))
                    Text(
                        text = row.id,
                        color = Palette.Dim,
                        fontFamily = FontFamily.Monospace,
                        style = MaterialTheme.typography.labelSmall,
                    )
                }
                Spacer(Modifier.height(2.dp))
                Text(
                    text = metaLine(row, nowMillis),
                    color = Palette.Dim,
                    fontFamily = FontFamily.Monospace,
                    style = MaterialTheme.typography.bodySmall,
                )
            }
            if (row.unseen) {
                Box(
                    modifier = Modifier
                        .background(Palette.Amber.copy(alpha = 0.18f), RoundedCornerShape(999.dp))
                        .padding(horizontal = 10.dp, vertical = 3.dp)
                        .testTag("unseen-${row.id}"),
                ) {
                    Text("unseen", color = Palette.Amber, style = MaterialTheme.typography.labelSmall)
                }
                Spacer(Modifier.width(4.dp))
            }
            IconButton(onClick = { menuOpen = true }, modifier = Modifier.testTag("card-menu-${row.id}")) {
                Icon(Icons.Default.MoreVert, contentDescription = "Machine menu", tint = Palette.Dim)
            }
            DropdownMenu(expanded = menuOpen, onDismissRequest = { menuOpen = false }) {
                DropdownMenuItem(
                    text = { Text(if (row.notifyEnabled) "Mute notifications" else "Unmute notifications") },
                    onClick = { menuOpen = false; onSetMuted(row.notifyEnabled) },
                )
                DropdownMenuItem(
                    text = { Text("Mark seen") },
                    onClick = { menuOpen = false; onMarkSeen() },
                )
                DropdownMenuItem(
                    text = { Text("Forget machine", color = Palette.Red) },
                    onClick = { menuOpen = false; onForget() },
                )
            }
        }
    }
}

/** "3 tabs · output 5m ago" / "offline since 2h ago" — pure formatting. */
fun metaLine(row: MachineRow, nowMillis: Long): String {
    if (!row.online) {
        val since = row.offlineSince?.let { Times.relative(it, nowMillis) } ?: "a while"
        return "offline since $since"
    }
    val parts = ArrayList<String>(2)
    row.tabCount?.let { parts.add(if (it == 1) "1 tab" else "$it tabs") }
    when {
        row.outputUnknown -> parts.add("activity unavailable — update termulaa on this machine")
        row.lastOutput != null -> parts.add("output ${Times.relative(row.lastOutput, nowMillis)}")
        // "never" is hidden per spec
    }
    return if (parts.isEmpty()) "online" else parts.joinToString(" · ")
}
