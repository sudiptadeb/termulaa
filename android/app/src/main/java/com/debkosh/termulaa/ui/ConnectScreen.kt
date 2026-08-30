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
 */
@Composable
fun ConnectScreen(
    busy: Boolean,
    error: String?,
    initialServerUrl: String = "https://memd.debkosh.com",
    onConnect: (serverUrl: String, username: String, password: String) -> Unit,
) {
    var serverUrl by rememberSaveable { mutableStateOf(initialServerUrl) }
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
            Spacer(Modifier.height(32.dp))

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

            if (error != null) {
                Spacer(Modifier.height(14.dp))
                Text(
                    text = error,
                    color = Palette.Red,
                    style = MaterialTheme.typography.bodySmall,
                    modifier = Modifier.fillMaxWidth().testTag("error"),
                )
            }

            Spacer(Modifier.height(22.dp))
            Button(
                onClick = { onConnect(serverUrl, username, password) },
                enabled = !busy,
                modifier = Modifier.fillMaxWidth().testTag("connect"),
            ) {
                if (busy) {
                    CircularProgressIndicator(
                        modifier = Modifier.height(18.dp),
                        strokeWidth = 2.dp,
                        color = Palette.Bg,
                    )
                } else {
                    Text("Connect")
                }
            }

            Spacer(Modifier.height(20.dp))
            Text(
                text = "Your memd account — the same login as the web dashboard.",
                style = MaterialTheme.typography.bodySmall,
                color = Palette.Dim,
            )
        }
    }
}
