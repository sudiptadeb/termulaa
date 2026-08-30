package com.debkosh.termulaa.store

import android.content.Context
import androidx.datastore.core.DataStore
import androidx.datastore.preferences.core.Preferences
import androidx.datastore.preferences.core.booleanPreferencesKey
import androidx.datastore.preferences.core.edit
import androidx.datastore.preferences.core.emptyPreferences
import androidx.datastore.preferences.core.intPreferencesKey
import androidx.datastore.preferences.core.stringPreferencesKey
import androidx.datastore.preferences.preferencesDataStore
import com.debkosh.termulaa.data.RememberedMachine
import com.debkosh.termulaa.data.WireJson
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.catch
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.flow.map
import kotlinx.serialization.builtins.ListSerializer
import kotlinx.serialization.builtins.MapSerializer
import kotlinx.serialization.builtins.serializer
import kotlinx.serialization.serializer
import java.io.IOException

private val Context.dataStore: DataStore<Preferences> by preferencesDataStore(name = "termulaa")

/**
 * Non-secret app state in DataStore(Preferences): server URL, the remembered
 * machine table, seen watermarks, last-notified watermarks, and prefs.
 * Collections are stored as JSON strings (kotlinx-serialization) — a corrupt
 * blob decodes to the empty default rather than crashing.
 */
class AppStore(private val context: Context) {

    private val machinesSer = ListSerializer(serializer<RememberedMachine>())
    private val longMapSer = MapSerializer(String.serializer(), Long.serializer())

    private val data: Flow<Preferences> = context.dataStore.data
        .catch { e -> if (e is IOException) emit(emptyPreferences()) else throw e }

    // ── flows ──────────────────────────────────────────────────────────────

    /** Normalized base URL, or null on first run / after sign-out. */
    val serverUrl: Flow<String?> = data.map { it[KEY_SERVER] }
    val machines: Flow<List<RememberedMachine>> = data.map { decodeMachines(it[KEY_MACHINES]) }
    val seenWatermarks: Flow<Map<String, Long>> = data.map { decodeLongMap(it[KEY_SEEN]) }
    val lastNotified: Flow<Map<String, Long>> = data.map { decodeLongMap(it[KEY_NOTIFIED]) }
    /** Background poll interval in minutes; first-run default 15. */
    val pollMinutes: Flow<Int> = data.map { it[KEY_POLL] ?: 15 }
    val offlineAlerts: Flow<Boolean> = data.map { it[KEY_OFFLINE_ALERTS] ?: true }
    val watchEnabled: Flow<Boolean> = data.map { it[KEY_WATCH] ?: false }

    // ── one-shot reads for workers/services ────────────────────────────────

    suspend fun serverUrlNow(): String? = serverUrl.first()
    suspend fun machinesNow(): List<RememberedMachine> = machines.first()
    suspend fun seenNow(): Map<String, Long> = seenWatermarks.first()
    suspend fun lastNotifiedNow(): Map<String, Long> = lastNotified.first()
    suspend fun offlineAlertsNow(): Boolean = offlineAlerts.first()
    suspend fun pollMinutesNow(): Int = pollMinutes.first()
    suspend fun watchEnabledNow(): Boolean = watchEnabled.first()

    // ── writers ────────────────────────────────────────────────────────────

    suspend fun setServerUrl(url: String?) = context.dataStore.edit {
        if (url == null) it.remove(KEY_SERVER) else it[KEY_SERVER] = url
    }

    suspend fun setMachines(machines: List<RememberedMachine>) = context.dataStore.edit {
        it[KEY_MACHINES] = WireJson.encodeToString(machinesSer, machines)
    }

    suspend fun setLastNotified(map: Map<String, Long>) = context.dataStore.edit {
        it[KEY_NOTIFIED] = WireJson.encodeToString(longMapSer, map)
    }

    /** Marks a machine seen: watermark = now, and drop its notified marker. */
    suspend fun markSeen(machineId: String, nowMillis: Long) = context.dataStore.edit { prefs ->
        val seen = decodeLongMap(prefs[KEY_SEEN]).toMutableMap()
        seen[machineId] = nowMillis
        prefs[KEY_SEEN] = WireJson.encodeToString(longMapSer, seen)
    }

    suspend fun setNotifyEnabled(machineId: String, enabled: Boolean) = editMachines { list ->
        list.map { if (it.id == machineId) it.copy(notifyEnabled = enabled) else it }
    }

    suspend fun forgetMachine(machineId: String) {
        editMachines { list -> list.filter { it.id != machineId } }
        context.dataStore.edit { prefs ->
            val seen = decodeLongMap(prefs[KEY_SEEN]).toMutableMap()
            seen.remove(machineId)
            prefs[KEY_SEEN] = WireJson.encodeToString(longMapSer, seen)
            val notified = decodeLongMap(prefs[KEY_NOTIFIED]).toMutableMap()
            notified.remove(machineId)
            prefs[KEY_NOTIFIED] = WireJson.encodeToString(longMapSer, notified)
        }
    }

    suspend fun setPollMinutes(minutes: Int) = context.dataStore.edit { it[KEY_POLL] = minutes }
    suspend fun setOfflineAlerts(on: Boolean) = context.dataStore.edit { it[KEY_OFFLINE_ALERTS] = on }
    suspend fun setWatchEnabled(on: Boolean) = context.dataStore.edit { it[KEY_WATCH] = on }

    /** Sign-out wipe of non-secret state (client.clearAuth() wipes secrets). */
    suspend fun clearAll() = context.dataStore.edit { it.clear() }

    private suspend fun editMachines(transform: (List<RememberedMachine>) -> List<RememberedMachine>) =
        context.dataStore.edit { prefs ->
            val current = decodeMachines(prefs[KEY_MACHINES])
            prefs[KEY_MACHINES] = WireJson.encodeToString(machinesSer, transform(current))
        }

    private fun decodeMachines(json: String?): List<RememberedMachine> {
        if (json.isNullOrBlank()) return emptyList()
        return try {
            WireJson.decodeFromString(machinesSer, json)
        } catch (_: Exception) {
            emptyList()
        }
    }

    private fun decodeLongMap(json: String?): Map<String, Long> {
        if (json.isNullOrBlank()) return emptyMap()
        return try {
            WireJson.decodeFromString(longMapSer, json)
        } catch (_: Exception) {
            emptyMap()
        }
    }

    companion object {
        private val KEY_SERVER = stringPreferencesKey("server_url")
        private val KEY_MACHINES = stringPreferencesKey("machines_json")
        private val KEY_SEEN = stringPreferencesKey("seen_json")
        private val KEY_NOTIFIED = stringPreferencesKey("notified_json")
        private val KEY_POLL = intPreferencesKey("poll_minutes")
        private val KEY_OFFLINE_ALERTS = booleanPreferencesKey("offline_alerts")
        private val KEY_WATCH = booleanPreferencesKey("watch_enabled")
    }
}
