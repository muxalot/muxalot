package dev.muxalot

import android.app.KeyguardManager
import android.content.Intent
import android.os.Bundle
import android.os.SystemClock
import androidx.activity.result.contract.ActivityResultContracts
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.activity.enableEdgeToEdge
import android.view.WindowManager
import android.widget.Toast
import androidx.compose.material3.Surface
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import dev.muxalot.data.AppLock
import dev.muxalot.data.AppSettings
import dev.muxalot.data.Server
import dev.muxalot.data.ServerStore
import dev.muxalot.data.ShortcutStore
import dev.muxalot.ui.FilesScreen
import dev.muxalot.ui.LockedScreen
import dev.muxalot.ui.PairScreen
import dev.muxalot.ui.ServerListScreen
import dev.muxalot.ui.SettingsScreen
import dev.muxalot.ui.ShortcutsScreen
import dev.muxalot.ui.TerminalScreen
import dev.muxalot.ui.theme.MuxalotTheme

private sealed interface Screen {
    data object List : Screen
    data class Pair(val url: String = "", val code: String = "") : Screen
    data class Term(val server: Server) : Screen
    data class Files(val server: Server) : Screen
    data class Shortcuts(val server: Server) : Screen
    data object Settings : Screen
}

class MainActivity : ComponentActivity() {
    private var screen by mutableStateOf<Screen>(Screen.List)
    private lateinit var appLock: AppLock
    private lateinit var settings: AppSettings
    private var lockEnabled by mutableStateOf(false)
    private var allowScreenshots by mutableStateOf(false)
    private var locked by mutableStateOf(false)
    private var promptOnStart = false
    private var pendingToggle = false
    private var stoppedAt: Long? = null

    private val credential = registerForActivityResult(ActivityResultContracts.StartActivityForResult()) { r ->
        stoppedAt = null // the prompt itself backgrounded us; don't count it as time away
        val ok = r.resultCode == RESULT_OK
        if (pendingToggle) {
            pendingToggle = false
            if (ok) { appLock.enabled = !appLock.enabled; lockEnabled = appLock.enabled }
        } else if (ok) locked = false
    }

    /** Device PIN/pattern/password prompt. Returns false when the phone has no screen lock. */
    private fun askCredential(): Boolean {
        val i = getSystemService(KeyguardManager::class.java).createConfirmDeviceCredentialIntent("Muxalot", null) ?: return false
        credential.launch(i)
        return true
    }

    /** FLAG_SECURE hides the window from screenshots, screen recording and the recents thumbnail. */
    private fun applyScreenshotSetting() {
        if (allowScreenshots) window.clearFlags(WindowManager.LayoutParams.FLAG_SECURE)
        else window.setFlags(WindowManager.LayoutParams.FLAG_SECURE, WindowManager.LayoutParams.FLAG_SECURE)
    }

    private fun unlock() {
        if (!askCredential()) locked = false // no screen lock left, so nothing to check against
    }

    /** Turning the lock on or off needs the credential too, or a thief could just switch it off. */
    private fun toggleLock() {
        pendingToggle = true
        if (!askCredential()) pendingToggle = false
    }

    override fun onStart() {
        super.onStart()
        val away = stoppedAt?.let { SystemClock.elapsedRealtime() - it > LOCK_GRACE_MS } ?: false
        stoppedAt = null
        if (appLock.enabled && away && !locked) { locked = true; promptOnStart = true }
        if (locked && promptOnStart) { promptOnStart = false; unlock() }
    }

    override fun onStop() {
        super.onStop()
        stoppedAt = SystemClock.elapsedRealtime()
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        enableEdgeToEdge()
        appLock = AppLock(this)
        settings = AppSettings(this)
        allowScreenshots = settings.allowScreenshots
        applyScreenshotSetting()
        lockEnabled = appLock.enabled
        locked = lockEnabled // cold start always locks
        promptOnStart = locked
        val store = ServerStore(this)
        val shortcuts = ShortcutStore(this)
        handlePairIntent(intent)
        setContent {
            MuxalotTheme {
                Surface {
                    if (locked) { LockedScreen(::unlock); return@Surface }
                    when (val s = screen) {
                        Screen.List -> ServerListScreen(
                            store, onSettings = { screen = Screen.Settings },
                            onOpen = { screen = Screen.Term(it) },
                            onAdd = { screen = Screen.Pair() },
                        )
                        is Screen.Pair -> PairScreen(
                            store, s.url, s.code,
                            onDone = { screen = Screen.List },
                            onCancel = { screen = Screen.List },
                        )
                        is Screen.Term -> TerminalScreen(
                            s.server,
                            onFiles = { screen = Screen.Files(s.server) },
                            onShortcuts = { screen = Screen.Shortcuts(s.server) },
                            onBack = { screen = Screen.List },
                        )
                        Screen.Settings -> SettingsScreen(
                            lockEnabled, ::toggleLock,
                            allowScreenshots, { allowScreenshots = it; settings.allowScreenshots = it; applyScreenshotSetting() },
                            onBack = { screen = Screen.List },
                        )
                        is Screen.Files -> FilesScreen(s.server, onBack = { screen = Screen.Term(s.server) })
                        is Screen.Shortcuts -> ShortcutsScreen(shortcuts, onBack = { screen = Screen.Term(s.server) })
                    }
                }
            }
        }
    }

    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        handlePairIntent(intent)
    }

    /** muxalot://pair?url=...&code=... (from a link or QR opened by the system camera). */
    private fun handlePairIntent(i: Intent?) {
        val d = i?.data ?: return
        if (d.scheme == "muxalot" && d.host == "pair") {
            // a link must not yank the user out of a live terminal or file transfer
            if (screen !is Screen.List && screen !is Screen.Pair && screen !is Screen.Settings) {
                Toast.makeText(this, "Close the terminal first to pair", Toast.LENGTH_SHORT).show()
                return
            }
            screen = Screen.Pair(d.getQueryParameter("url").orEmpty(), d.getQueryParameter("code").orEmpty())
        }
    }

    private companion object {
        const val LOCK_GRACE_MS = 60_000L
    }
}
