package com.debkosh.termulaa.ui

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.imePadding
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Button
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.text.input.PasswordVisualTransformation
import androidx.compose.ui.unit.dp

/**
 * Stateless Connect screen — all state hoisted so the Robolectric test can
 * drive it without the app graph.
 *
 * Primary path: server URL + pairing code (works for every account type,
 * including Google/SSO — the code is minted by the already-signed-in
 * dashboard). Secondary: a collapsed username/password form for servers with
 * local accounts. Field state is keyed on the prefill values so a deep link
 * arriving while the screen is open updates the form.
 */
@Composable
fun ConnectScreen(
    busy: Boolean,
    error: String?,
    notice: String? = null,
    initialServerUrl: String = "https://memd.debkosh.com",
    initialCode: String = "",
    onPair: (serverUrl: String, code: String) -> Unit,
    onPasswordSignIn: (serverUrl: String, username: String, password: String) -> Unit,
) {
    var serverUrl by rememberSaveable(initialServerUrl) { mutableStateOf(initialServerUrl) }
    var code by rememberSaveable(initialCode) { mutableStateOf(initialCode) }
    var showPassword by rememberSaveable { mutableStateOf(false) }
    var username by rememberSaveable { mutableStateOf("") }
    var password by rememberSaveable { mutableStateOf("") }

    Surface(modifier = Modifier.fillMaxSize(), color = Palette.Bg) {
        Column(
            modifier = Modifier
                .fillMaxSize()
                .verticalScroll(rememberScrollState())
                .imePadding()
                .padding(horizontal = 28.dp, vertical = 48.dp),
            verticalArrangement = Arrangement.Center,
            horizontalAlignment = Alignment.CenterHorizontally,
        ) {
            Text(
                text = ">_ termulaa",
                style = MaterialTheme.typography.headlineMedium,
                fontFamily = FontFamily.Monospace,
                color = Palette.Green,
            )
            Spacer(Modifier.height(6.dp))
            Text(
                text = "remote terminals on your phone",
                style = MaterialTheme.typography.bodyMedium,
                color = Palette.Dim,
            )
            Spacer(Modifier.height(28.dp))

            if (notice != null) {
                Text(
                    text = notice,
                    color = Palette.Amber,
                    style = MaterialTheme.typography.bodySmall,
                    modifier = Modifier.fillMaxWidth().testTag("notice"),
                )
                Spacer(Modifier.height(14.dp))
            }

            OutlinedTextField(
                value = serverUrl,
                onValueChange = { serverUrl = it },
                label = { Text("Server URL") },
                singleLine = true,
                enabled = !busy,
                keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Uri),
                modifier = Modifier.fillMaxWidth().testTag("server"),
            )
            Spacer(Modifier.height(12.dp))
            OutlinedTextField(
                value = code,
                onValueChange = { code = it },
                label = { Text("Pairing code") },
                placeholder = { Text("XXX-XXX-XXX", fontFamily = FontFamily.Monospace) },
                singleLine = true,
                enabled = !busy,
                keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Ascii),
                modifier = Modifier.fillMaxWidth().testTag("code"),
            )
            Spacer(Modifier.height(8.dp))
            Text(
                text = "Get a code from the termulaa section of your dashboard — " +
                    "works with Google/SSO sign-in",
                style = MaterialTheme.typography.bodySmall,
                color = Palette.Dim,
                modifier = Modifier.fillMaxWidth().testTag("codeHelp"),
            )

            if (error != null) {
                Spacer(Modifier.height(14.dp))
                Text(
                    text = error,
                    color = Palette.Red,
                    style = MaterialTheme.typography.bodySmall,
                    modifier = Modifier.fillMaxWidth().testTag("error"),
                )
            }

            Spacer(Modifier.height(18.dp))
            Button(
                onClick = { onPair(serverUrl, code) },
                enabled = !busy,
                modifier = Modifier.fillMaxWidth().testTag("pair"),
            ) {
                if (busy) {
                    CircularProgressIndicator(
                        modifier = Modifier.height(18.dp),
                        strokeWidth = 2.dp,
                        color = Palette.Bg,
                    )
                } else {
                    Text("Pair")
                }
            }

            Spacer(Modifier.height(18.dp))
            TextButton(
                onClick = { showPassword = !showPassword },
                enabled = !busy,
                modifier = Modifier.testTag("passwordToggle"),
            ) {
                Text(
                    if (showPassword) "Hide password sign-in"
                    else "Sign in with password instead",
                    color = Palette.Dim,
                )
            }

            if (showPassword) {
                Spacer(Modifier.height(6.dp))
                OutlinedTextField(
                    value = username,
                    onValueChange = { username = it },
                    label = { Text("Username") },
                    singleLine = true,
                    enabled = !busy,
                    modifier = Modifier.fillMaxWidth().testTag("username"),
                )
                Spacer(Modifier.height(12.dp))
                OutlinedTextField(
                    value = password,
                    onValueChange = { password = it },
                    label = { Text("Password") },
                    singleLine = true,
                    enabled = !busy,
                    visualTransformation = PasswordVisualTransformation(),
                    keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Password),
                    modifier = Modifier.fillMaxWidth().testTag("password"),
                )
                Spacer(Modifier.height(16.dp))
                Button(
                    onClick = { onPasswordSignIn(serverUrl, username, password) },
                    enabled = !busy,
                    modifier = Modifier.fillMaxWidth().testTag("connect"),
                ) {
                    Text("Connect")
                }
                Spacer(Modifier.height(8.dp))
                Text(
                    text = "Password sign-in works only for local memd accounts.",
                    style = MaterialTheme.typography.bodySmall,
                    color = Palette.Dim,
                )
            }
        }
    }
}
