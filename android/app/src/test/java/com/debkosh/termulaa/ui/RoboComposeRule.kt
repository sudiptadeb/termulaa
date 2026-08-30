package com.debkosh.termulaa.ui

import androidx.activity.ComponentActivity
import androidx.compose.ui.test.junit4.AndroidComposeTestRule
import org.junit.rules.ExternalResource
import org.robolectric.Robolectric
import org.robolectric.android.controller.ActivityController

/**
 * Compose rule for Robolectric against the RELEASE variant.
 *
 * createComposeRule() launches its host activity through ActivityScenario,
 * which requires the activity to be declared in the app's merged manifest —
 * fine for debug builds (ui-test-manifest merges it in) but the release
 * manifest deliberately ships without test scaffolding. Robolectric's own
 * ActivityController does not need a manifest entry, so we drive the standard
 * AndroidComposeTestRule with it instead.
 */
class RoboActivityRule : ExternalResource() {
    private var controller: ActivityController<ComponentActivity>? = null
    val activity: ComponentActivity
        get() = checkNotNull(controller?.get()) { "activity not launched yet" }

    override fun before() {
        controller = Robolectric.buildActivity(ComponentActivity::class.java).setup()
    }

    override fun after() {
        try {
            controller?.pause()?.stop()?.destroy()
        } catch (_: Exception) {
        }
        controller = null
    }
}

fun createRoboComposeRule(): AndroidComposeTestRule<RoboActivityRule, ComponentActivity> =
    AndroidComposeTestRule(
        activityRule = RoboActivityRule(),
        activityProvider = { rule -> rule.activity },
    )
