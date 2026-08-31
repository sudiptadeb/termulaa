package com.debkosh.termulaa.ui

import android.Manifest
import android.content.Intent
import androidx.core.net.toUri
import android.os.Build
import android.provider.Settings
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.padding
import androidx.compose.material3.Button
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.SnackbarHostState
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.lifecycle.compose.LocalLifecycleOwner
import androidx.compose.ui.unit.dp
import androidx.core.content.ContextCompat
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.LifecycleEventObserver
import androidx.lifecycle.viewmodel.compose.viewModel
import androidx.navigation.NavHostController
import androidx.navigation.compose.NavHost
import androidx.navigation.compose.composable
import androidx.navigation.compose.rememberNavController
import com.debkosh.termulaa.AppGraph
import com.debkosh.termulaa.core.Urls
import com.debkosh.termulaa.watch.WatchService
import com.debkosh.termulaa.work.CheckWorker
import kotlinx.coroutines.launch

object Routes {
    const val CONNECT = "connect"
    const val MACHINES = "machines"
    const val SETTINGS = "settings"
    const val TERMINAL = "terminal/{machineId}"
    fun terminal(machineId: String) = "terminal/$machineId"
}

@Composable
fun TermulaaNav(
    graph: AppGraph,
    startSignedIn: Boolean,
    versionName: String,
    pendingMachineId: String?,
    onDeepLinkConsumed: () -> Unit,
    pendingPairUri: String? = null,
    onPairLinkConsumed: () -> Unit = {},
) {
    val nav = rememberNavController()
    val context = LocalContext.current

    // Notification deep-link: navigate whenever MainActivity hands us an id.
    LaunchedEffect(pendingMachineId) {
        if (pendingMachineId != null) {
            nav.navigate(Routes.terminal(pendingMachineId)) { launchSingleTop = true }
            onDeepLinkConsumed()
        }
    }

    // Pairing deep-link (termulaa://pair): make sure the Connect screen is up;
    // its composable consumes the URI and decides prefill vs auto-redeem.
    LaunchedEffect(pendingPairUri) {
        if (pendingPairUri != null && nav.currentDestination?.route != Routes.CONNECT) {
            nav.navigate(Routes.CONNECT) { launchSingleTop = true }
        }
    }

    NavHost(
        navController = nav,
        startDestination = if (startSignedIn) Routes.MACHINES else Routes.CONNECT,
    ) {
        composable(Routes.CONNECT) {
            val vm: ConnectViewModel = viewModel(initializer = { ConnectViewModel(graph) })
            val state by vm.state.collectAsState()
            val onSignedIn = {
                CheckWorker.schedule(context, 15)
                nav.navigate(Routes.MACHINES) {
                    popUpTo(Routes.CONNECT) { inclusive = true }
                }
            }
            LaunchedEffect(pendingPairUri) {
                if (pendingPairUri != null) {
                    vm.applyPairLink(pendingPairUri, onSuccess = onSignedIn)
                    onPairLinkConsumed()
                }
            }
            ConnectScreen(
                busy = state.busy,
                error = state.error,
                notice = state.notice,
                initialServerUrl = state.prefillServer
                    ?: graph.currentBase() ?: "https://memd.debkosh.com",
                initialCode = state.prefillCode ?: "",
                onPair = { url, code -> vm.pair(url, code, onSuccess = onSignedIn) },
                onPasswordSignIn = { url, user, pass ->
                    vm.connect(url, user, pass, onSuccess = onSignedIn)
                },
            )
        }

        composable(Routes.MACHINES) {
            val vm: MachinesViewModel = viewModel(initializer = { MachinesViewModel(graph) })
            val state by vm.state.collectAsState()
            val snackbar = remember { SnackbarHostState() }
            val scope = rememberCoroutineScope()

            NotificationPermissionRequest()
            PollWhileResumed(vm)

            MachinesScreen(
                state = state,
                snackbarHostState = snackbar,
                onRefresh = vm::refresh,
                onOpenMachine = { row -> nav.navigate(Routes.terminal(row.id)) },
                onOfflineTap = { row ->
                    scope.launch {
                        snackbar.showSnackbar(
                            "${row.label.ifBlank { row.id }} is offline — it will come back " +
                                "when the machine reconnects to the server."
                        )
                    }
                },
                onMarkSeen = vm::markSeen,
                onSetMuted = { id, mute -> vm.setMuted(id, mute) },
                onForget = vm::forget,
                onWatchToggle = { on ->
                    vm.setWatchEnabled(on)
                    if (on) WatchService.start(context) else WatchService.stop(context)
                },
                onSettings = { nav.navigate(Routes.SETTINGS) },
                onSignOut = { signOut(graph, nav, context) },
                onGoConnect = {
                    nav.navigate(Routes.CONNECT) { popUpTo(Routes.MACHINES) { inclusive = true } }
                },
                onOpenDashboard = {
                    val base = graph.currentBase() ?: return@MachinesScreen
                    openUrl(context, Urls.join(base, "/#/termulaa"))
                },
            )
        }

        composable(Routes.SETTINGS) {
            val store = graph.store
            val poll by store.pollMinutes.collectAsState(initial = 15)
            val offline by store.offlineAlerts.collectAsState(initial = true)
            val scope = rememberCoroutineScope()
            SettingsScreen(
                serverUrl = graph.currentBase() ?: "",
                versionName = versionName,
                pollMinutes = poll,
                offlineAlerts = offline,
                onBack = { nav.popBackStack() },
                onPollMinutes = { m ->
                    scope.launch {
                        store.setPollMinutes(m)
                        CheckWorker.schedule(context, m)
                    }
                },
                onOfflineAlerts = { on -> scope.launch { store.setOfflineAlerts(on) } },
                onSignOut = { signOut(graph, nav, context) },
                onNotificationSettings = {
                    try {
                        context.startActivity(
                            Intent(Settings.ACTION_APP_NOTIFICATION_SETTINGS)
                                .putExtra(Settings.EXTRA_APP_PACKAGE, context.packageName)
                        )
                    } catch (_: Exception) {
                    }
                },
                onOpenProject = { openUrl(context, "https://github.com/sudiptadeb/termulaa") },
            )
        }

        composable(Routes.TERMINAL) { entry ->
            val machineId = entry.arguments?.getString("machineId") ?: ""
            val vm: TerminalViewModel =
                viewModel(initializer = { TerminalViewModel(graph) })
            val state by vm.state.collectAsState()

            LaunchedEffect(machineId) {
                if (machineId.isBlank()) nav.popBackStack() else vm.load(machineId)
            }
            // Leaving the terminal also bumps the seen watermark.
            DisposableEffect(machineId) {
                onDispose { if (machineId.isNotBlank()) vm.markSeen(machineId) }
            }

            when (val s = state) {
                TerminalState.Loading -> CenteredMessage { CircularProgressIndicator(color = Palette.Green) }
                is TerminalState.Ready -> TerminalScreen(
                    label = s.label,
                    terminalUrl = s.url,
                    onBack = { nav.popBackStack() },
                    onAuthFailed = { vm.reloginForWebView() },
                )
                is TerminalState.Gone -> CenteredMessage {
                    Text("${s.label.ifBlank { "(unnamed)" }} is offline", color = Palette.Red)
                    Text(
                        "The machine must reconnect to the server first.",
                        color = Palette.Dim,
                        modifier = Modifier.padding(top = 6.dp),
                    )
                    Button(onClick = { nav.popBackStack() }, modifier = Modifier.padding(top = 18.dp)) {
                        Text("Back")
                    }
                }
                is TerminalState.Failed -> CenteredMessage {
                    Text(s.message, color = Palette.Red)
                    Button(onClick = { nav.popBackStack() }, modifier = Modifier.padding(top = 18.dp)) {
                        Text("Back")
                    }
                }
            }
        }
    }
}

@Composable
private fun CenteredMessage(content: @Composable () -> Unit) {
    Column(
        modifier = Modifier.fillMaxSize().padding(32.dp),
        verticalArrangement = Arrangement.Center,
        horizontalAlignment = Alignment.CenterHorizontally,
    ) { content() }
}

/** Ask for POST_NOTIFICATIONS once, on first reach of Machines (API 33+). */
@Composable
private fun NotificationPermissionRequest() {
    if (Build.VERSION.SDK_INT < 33) return
    val context = LocalContext.current
    val launcher = rememberLauncherForActivityResult(
        ActivityResultContracts.RequestPermission()
    ) { /* denied is fine — the app just stays silent */ }
    LaunchedEffect(Unit) {
        val granted = ContextCompat.checkSelfPermission(
            context, Manifest.permission.POST_NOTIFICATIONS
        ) == android.content.pm.PackageManager.PERMISSION_GRANTED
        if (!granted) launcher.launch(Manifest.permission.POST_NOTIFICATIONS)
    }
}

/** Start/stop the 10s auto-poll with the RESUMED lifecycle state. */
@Composable
private fun PollWhileResumed(vm: MachinesViewModel) {
    val owner = LocalLifecycleOwner.current
    DisposableEffect(owner, vm) {
        val observer = LifecycleEventObserver { _, event ->
            when (event) {
                Lifecycle.Event.ON_RESUME -> vm.startPolling()
                Lifecycle.Event.ON_PAUSE -> vm.stopPolling()
                else -> Unit
            }
        }
        owner.lifecycle.addObserver(observer)
        onDispose {
            owner.lifecycle.removeObserver(observer)
            vm.stopPolling()
        }
    }
}

private fun signOut(graph: AppGraph, nav: NavHostController, context: android.content.Context) {
    graph.scope.launch {
        // Best-effort DELETE /api/app/tokens/self (keeps the dashboard's
        // "paired phones" list honest), then the unconditional local wipe.
        graph.client.signOut()
        graph.store.clearAll()
        graph.setBase(null)
        CheckWorker.cancel(context)
        WatchService.stop(context)
        try {
            android.webkit.CookieManager.getInstance().removeAllCookies(null)
        } catch (_: Throwable) {
        }
    }
    nav.navigate(Routes.CONNECT) { popUpTo(0) { inclusive = true } }
}

private fun openUrl(context: android.content.Context, url: String) {
    try {
        context.startActivity(Intent(Intent.ACTION_VIEW, url.toUri()))
    } catch (_: Exception) {
    }
}
