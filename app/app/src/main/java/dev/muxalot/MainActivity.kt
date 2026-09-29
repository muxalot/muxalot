package dev.muxalot

import android.content.Intent
import android.os.Bundle
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.activity.enableEdgeToEdge
import androidx.compose.material3.Surface
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import dev.muxalot.data.Server
import dev.muxalot.data.ServerStore
import dev.muxalot.data.ShortcutStore
import dev.muxalot.ui.FilesScreen
import dev.muxalot.ui.PairScreen
import dev.muxalot.ui.ServerListScreen
import dev.muxalot.ui.ShortcutsScreen
import dev.muxalot.ui.TerminalScreen
import dev.muxalot.ui.theme.MuxalotTheme

private sealed interface Screen {
    data object List : Screen
    data class Pair(val url: String = "", val code: String = "") : Screen
    data class Term(val server: Server) : Screen
    data class Files(val server: Server) : Screen
    data class Shortcuts(val server: Server) : Screen
}

class MainActivity : ComponentActivity() {
    private var screen by mutableStateOf<Screen>(Screen.List)

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        enableEdgeToEdge()
        val store = ServerStore(this)
        val shortcuts = ShortcutStore(this)
        handlePairIntent(intent)
        setContent {
            MuxalotTheme {
                Surface {
                    when (val s = screen) {
                        Screen.List -> ServerListScreen(
                            store,
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
            screen = Screen.Pair(d.getQueryParameter("url").orEmpty(), d.getQueryParameter("code").orEmpty())
        }
    }
}
