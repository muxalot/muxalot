package dev.muxalot.data

import android.content.Context
import android.security.keystore.KeyGenParameterSpec
import android.security.keystore.KeyProperties
import android.util.Base64
import dev.muxalot.BuildConfig
import kotlinx.serialization.Serializable
import kotlinx.serialization.encodeToString
import kotlinx.serialization.json.Json
import java.security.KeyPairGenerator
import java.security.KeyStore
import java.security.PrivateKey
import java.security.Signature
import java.security.spec.ECGenParameterSpec

/** A paired server. The private key lives in Android Keystore under [id]. */
@Serializable
data class Server(val id: String, val name: String, val url: String, val deviceId: String)

class ServerStore(ctx: Context) {
    private val prefs = ctx.getSharedPreferences("servers", Context.MODE_PRIVATE)
    private val json = Json { ignoreUnknownKeys = true }

    fun load(): List<Server> = try {
        json.decodeFromString(prefs.getString("list", "[]")!!)
    } catch (e: Exception) {
        emptyList()
    }

    private fun save(list: List<Server>) {
        prefs.edit().putString("list", json.encodeToString(list)).apply()
    }

    fun add(s: Server) = save(load() + s)

    fun remove(id: String) {
        save(load().filter { it.id != id })
        DeviceKey.delete(id)
    }
}

/** Opt-in device-credential lock for the whole app; enforced in MainActivity. */
class AppLock(ctx: Context) {
    private val prefs = ctx.getSharedPreferences("applock", Context.MODE_PRIVATE)
    var enabled: Boolean
        get() = prefs.getBoolean("enabled", false)
        set(v) = prefs.edit().putBoolean("enabled", v).apply()
}

/** Screenshots and screen recording are blocked (FLAG_SECURE) unless allowed here; debug builds allow them so adb screencap works. */
class AppSettings(ctx: Context) {
    private val prefs = ctx.getSharedPreferences("settings", Context.MODE_PRIVATE)
    var allowScreenshots: Boolean
        get() = prefs.getBoolean("allow_screenshots", BuildConfig.DEBUG)
        set(v) = prefs.edit().putBoolean("allow_screenshots", v).apply()
}

/**
 * Per-server ECDSA P-256 key pair in the Android Keystore (hardware-backed
 * where available). The private key never leaves the device; only the public
 * key (X.509 SubjectPublicKeyInfo, base64) is sent to the server at pairing.
 */
object DeviceKey {
    private const val PROVIDER = "AndroidKeyStore"
    private fun alias(id: String) = "muxalot_$id"
    private fun ks() = KeyStore.getInstance(PROVIDER).apply { load(null) }

    /** Creates a key pair for [id] and returns the base64 public key. */
    fun create(id: String): String {
        val kpg = KeyPairGenerator.getInstance(KeyProperties.KEY_ALGORITHM_EC, PROVIDER)
        kpg.initialize(
            KeyGenParameterSpec.Builder(alias(id), KeyProperties.PURPOSE_SIGN)
                .setAlgorithmParameterSpec(ECGenParameterSpec("secp256r1"))
                .setDigests(KeyProperties.DIGEST_SHA256)
                .build()
        )
        return Base64.encodeToString(kpg.generateKeyPair().public.encoded, Base64.NO_WRAP)
    }

    fun delete(id: String) {
        runCatching { ks().deleteEntry(alias(id)) }
    }

    /** SHA256withECDSA, ASN.1 DER output (what the Go agent verifies). */
    fun sign(id: String, msg: ByteArray): ByteArray {
        val key = ks().getKey(alias(id), null) as PrivateKey
        return Signature.getInstance("SHA256withECDSA").run {
            initSign(key)
            update(msg)
            sign()
        }
    }
}

/** Terminal color presets (pro). Values are xterm.js theme objects; the ANSI palette stays xterm's default. */
object Themes {
    val presets = linkedMapOf(
        "Default" to """{"background":"#0b0f14","foreground":"#d8dee9","cursor":"#ffcc66"}""",
        "Dracula" to """{"background":"#282a36","foreground":"#f8f8f2","cursor":"#f8f8f0"}""",
        "Nord" to """{"background":"#2e3440","foreground":"#d8dee9","cursor":"#88c0d0"}""",
        "Solarized Dark" to """{"background":"#002b36","foreground":"#839496","cursor":"#93a1a1"}""",
        "Gruvbox Dark" to """{"background":"#282828","foreground":"#ebdbb2","cursor":"#fe8019"}""",
        "Light" to """{"background":"#ffffff","foreground":"#24292f","cursor":"#0969da","selectionBackground":"#b6d6ff"}""",
    )
}

class ThemeStore(ctx: Context) {
    private val prefs = ctx.getSharedPreferences("theme", Context.MODE_PRIVATE)

    fun name(): String = prefs.getString("name", null)?.takeIf { it in Themes.presets } ?: "Default"

    fun setName(n: String) {
        prefs.edit().putString("name", n).apply()
    }

    fun themeJson(): String = Themes.presets.getValue(name())
}

/** A user-defined fan key: [label] is the short face text, [text] is typed into the terminal. */
@Serializable
data class Shortcut(val label: String, val text: String, val enter: Boolean)

class ShortcutStore(ctx: Context) {
    private val prefs = ctx.getSharedPreferences("shortcuts", Context.MODE_PRIVATE)
    private val json = Json { ignoreUnknownKeys = true }

    /** Defaults until the user first saves a list. */
    fun load(): List<Shortcut> =
        prefs.getString("list", null)?.let { runCatching { json.decodeFromString<List<Shortcut>>(it) }.getOrNull() } ?: DEFAULTS

    fun save(list: List<Shortcut>) {
        prefs.edit().putString("list", json.encodeToString(list)).apply()
    }

    fun toJson(list: List<Shortcut>): String = json.encodeToString(list)

    /** Throws on malformed input. */
    fun parse(text: String): List<Shortcut> = json.decodeFromString(text)

    companion object {
        val DEFAULTS = listOf(
            Shortcut("cl", "claude", true),
            Shortcut("cla", "claudea", true),
            Shortcut("/clr", "/clear", true),
            Shortcut("/sc:", "/sc:", false),
            Shortcut("/exit", "/exit", true),
        )
    }
}
