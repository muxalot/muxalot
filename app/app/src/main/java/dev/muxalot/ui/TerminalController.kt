package dev.muxalot.ui

import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateListOf
import androidx.compose.runtime.mutableStateMapOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import dev.muxalot.data.Server
import dev.muxalot.net.Api
import dev.muxalot.net.ConnState
import dev.muxalot.net.TerminalConnection
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext

private val SESSION_RE = Regex("^[A-Za-z0-9_-]{1,32}$")

/** Tabs for one server. Each tab is a tmux session on that server. */
class TerminalController(val server: Server, private val scope: CoroutineScope) {
    val api = Api(server)
    val tabs = mutableStateListOf<String>()
    var selected by mutableStateOf<String?>(null)
    var error by mutableStateOf<String?>(null)
    val states = mutableStateMapOf<String, ConnState>()

    private val conns = HashMap<String, TerminalConnection>()
    private val encs = HashMap<String, InputEncoder>()

    fun conn(name: String): TerminalConnection = conns.getOrPut(name) {
        TerminalConnection(server, name).also { c -> c.onState = { s -> states[name] = s } }
    }

    fun enc(name: String): InputEncoder = encs.getOrPut(name) { InputEncoder(conn(name)) }

    /** Open a tab for every existing tmux session (or "main" if there are none). */
    fun load() {
        scope.launch {
            try {
                val list = withContext(Dispatchers.IO) { api.sessions() }
                list.filter { SESSION_RE.matches(it.name) }.forEach { if (it.name !in tabs) tabs.add(it.name) }
                error = null
            } catch (e: Exception) {
                error = e.message ?: e.javaClass.simpleName
            }
            if (tabs.isEmpty()) tabs.add("main")
            if (selected == null || selected !in tabs) selected = tabs.first()
        }
    }

    fun validName(name: String) = SESSION_RE.matches(name)

    fun addTab(name: String) {
        if (!validName(name)) return
        if (name !in tabs) tabs.add(name)
        selected = name
    }

    /** Detach (session keeps running) or kill it on the server. */
    fun closeTab(name: String, kill: Boolean) {
        conns.remove(name)?.close()
        encs.remove(name)
        states.remove(name)
        val idx = tabs.indexOf(name)
        tabs.remove(name)
        if (selected == name) selected = tabs.getOrNull(minOf(idx, tabs.size - 1))
        if (kill) scope.launch {
            try {
                withContext(Dispatchers.IO) { api.kill(name) }
            } catch (e: Exception) {
                error = "Kill failed: ${e.message}"
            }
        }
    }

    fun kick() = conns.values.toList().forEach { it.kick() }

    /** Rename the tmux session on the server; the WS stays attached through the rename. */
    fun renameTab(from: String, to: String) {
        if (!validName(to) || from == to) return
        scope.launch {
            try {
                withContext(Dispatchers.IO) { api.rename(from, to) }
            } catch (e: Exception) {
                error = "Rename failed: ${e.message}"
                return@launch
            }
            conns.remove(from)?.let {
                it.session = to // so a later kick() reconnects to the new name, not a resurrected old one
                it.onState = { s -> states[to] = s }
                conns[to] = it
            }
            encs.remove(from)?.let { encs[to] = it }
            states[from]?.let { states[to] = it }
            states.remove(from)
            val i = tabs.indexOf(from)
            if (i >= 0) tabs[i] = to
            if (selected == from) selected = to
        }
    }

    fun dispose() {
        conns.values.toList().forEach { it.close() }
        conns.clear()
    }
}
