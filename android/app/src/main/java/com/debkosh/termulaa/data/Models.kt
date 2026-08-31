package com.debkosh.termulaa.data

import kotlinx.serialization.Serializable
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonObject

/**
 * Wire models for the frozen memd/termulaa server contract. Property names
 * match the JSON exactly (snake_case on the wire) so no @SerialName churn;
 * every model tolerates unknown keys via [WireJson].
 */
val WireJson: Json = Json {
    ignoreUnknownKeys = true
    isLenient = false
    coerceInputValues = true
}

@Serializable
data class LoginRequest(val username: String, val password: String)

@Serializable
data class ApiError(val error: String? = null)

/** POST /api/app/redeem — pairing-code exchange (no auth). */
@Serializable
data class RedeemRequest(val code: String, val label: String)

/** 200 body of /api/app/redeem: the long-lived revocable app token. */
@Serializable
data class RedeemResponse(val token: String? = null, val user: JsonObject? = null)

@Serializable
data class SessionAuth(val oidc_enabled: Boolean = false)

@Serializable
data class SessionFeatures(val rc: Boolean = false)

/** GET /api/session — user == null means signed out. */
@Serializable
data class SessionInfo(
    val auth: SessionAuth? = null,
    val features: SessionFeatures? = null,
    val user: JsonObject? = null,
) {
    val signedIn: Boolean get() = user != null
    val rcEnabled: Boolean get() = features?.rc == true
}

/** One currently-connected machine from GET /rc/api/agents. */
@Serializable
data class Agent(
    val id: String,
    val label: String = "",
    val port: Int = 0,
    val tunnels: Int = 0,
    val connected_at: String = "",
    /** Present only in path mode (view_host == ""). e.g. "/rc/t/<64hex>/". */
    val url: String? = null,
)

@Serializable
data class AgentsResponse(
    val agents: List<Agent> = emptyList(),
    val view_host: String = "",
)

/**
 * One tab from the proxied GET <agent.url>api/tabs.
 * last_output == null ⇒ the field was ABSENT (older agent) ⇒ activity unknown.
 */
@Serializable
data class TabInfo(
    val id: String,
    val name: String = "",
    val pane_count: Int = 0,
    val alive: Boolean = false,
    val last_active: String = "",
    val last_output: String? = null,
)
