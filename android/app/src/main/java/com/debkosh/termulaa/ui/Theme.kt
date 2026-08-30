package com.debkosh.termulaa.ui

import androidx.compose.foundation.isSystemInDarkTheme
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.darkColorScheme
import androidx.compose.runtime.Composable
import androidx.compose.ui.graphics.Color

/** termulaa web-UI palette — the app is dark-only by design. */
object Palette {
    val Bg = Color(0xFF0F1115)
    val Surface = Color(0xFF1A1D23)
    val Border = Color(0xFF2A2E37)
    val Text = Color(0xFFE6E6E6)
    val Dim = Color(0xFF8B919E)
    val Green = Color(0xFF34D399)
    val Amber = Color(0xFFFBBF24)
    val Red = Color(0xFFF87171)
}

private val TerminalColors = darkColorScheme(
    primary = Palette.Green,
    onPrimary = Palette.Bg,
    secondary = Palette.Amber,
    onSecondary = Palette.Bg,
    error = Palette.Red,
    onError = Palette.Bg,
    background = Palette.Bg,
    onBackground = Palette.Text,
    surface = Palette.Surface,
    onSurface = Palette.Text,
    surfaceVariant = Palette.Surface,
    onSurfaceVariant = Palette.Dim,
    outline = Palette.Border,
    surfaceContainer = Palette.Surface,
    surfaceContainerHigh = Palette.Surface,
    surfaceContainerHighest = Palette.Surface,
    surfaceContainerLow = Palette.Bg,
    surfaceContainerLowest = Palette.Bg,
)

@Composable
fun TermulaaTheme(content: @Composable () -> Unit) {
    // Dark-only: ignore isSystemInDarkTheme deliberately.
    isSystemInDarkTheme()
    MaterialTheme(colorScheme = TerminalColors, content = content)
}
