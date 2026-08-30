package com.debkosh.termulaa.net

import android.content.Context
import android.content.SharedPreferences
import androidx.core.content.edit
import androidx.security.crypto.EncryptedSharedPreferences
import androidx.security.crypto.MasterKey

/**
 * Tiny key/value secret store. Backed by EncryptedSharedPreferences in
 * production; tests use [InMemorySecretStore]. Only the memd credentials and
 * the memd_session cookie live here — everything else is plain DataStore.
 */
interface SecretStore {
    fun get(key: String): String?
    fun put(key: String, value: String?)
}

class InMemorySecretStore : SecretStore {
    private val map = HashMap<String, String>()
    @Synchronized override fun get(key: String): String? = map[key]
    @Synchronized override fun put(key: String, value: String?) {
        if (value == null) map.remove(key) else map[key] = value
    }
}

/**
 * EncryptedSharedPreferences-backed store. Keystore corruption is a real
 * (rare) device state: on failure we wipe the pref file once and retry; if it
 * still fails we degrade to an in-memory store for this process — the user
 * simply has to sign in again next launch, the app must not crash.
 */
class EncryptedSecretStore(context: Context) : SecretStore {

    private val delegate: SecretStore = try {
        PrefsBacked(open(context))
    } catch (_: Exception) {
        try {
            context.deleteSharedPreferences(FILE)
            PrefsBacked(open(context))
        } catch (_: Exception) {
            InMemorySecretStore()
        }
    }

    private fun open(context: Context): SharedPreferences {
        val masterKey = MasterKey.Builder(context)
            .setKeyScheme(MasterKey.KeyScheme.AES256_GCM)
            .build()
        return EncryptedSharedPreferences.create(
            context,
            FILE,
            masterKey,
            EncryptedSharedPreferences.PrefKeyEncryptionScheme.AES256_SIV,
            EncryptedSharedPreferences.PrefValueEncryptionScheme.AES256_GCM,
        )
    }

    private class PrefsBacked(private val prefs: SharedPreferences) : SecretStore {
        override fun get(key: String): String? = try {
            prefs.getString(key, null)
        } catch (_: Exception) {
            null
        }
        override fun put(key: String, value: String?) {
            try {
                prefs.edit {
                    if (value == null) remove(key) else putString(key, value)
                }
            } catch (_: Exception) {
                // Losing a secret write degrades to "sign in again"; never crash.
            }
        }
    }

    override fun get(key: String): String? = delegate.get(key)
    override fun put(key: String, value: String?) = delegate.put(key, value)

    companion object {
        private const val FILE = "termulaa_secrets"
    }
}
