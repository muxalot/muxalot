package dev.muxalot.ui

import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateListOf
import androidx.compose.runtime.mutableStateMapOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import dev.muxalot.data.Server
import dev.muxalot.data.TabColorStore
import dev.muxalot.net.Api
import dev.muxalot.net.ConnState
import dev.muxalot.net.TerminalConnection
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext

private val SESSION_RE = Regex("^[A-Za-z0-9_-]{1,32}$")

/** Tabs for one server. Each tab is a tmux session on that server. */
class TerminalController(val server: Server, private val scope: CoroutineScope, private val colorStore: TabColorStore) {
    val api = Api(server)
    val tabs = mutableStateListOf<String>()
    var selected by mutableStateOf<String?>(null)
    var error by mutableStateOf<String?>(null)
    val states = mutableStateMapOf<String, ConnState>()
    val tabColors = mutableStateMapOf<String, Int>() // ARGB; chosen per device

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
                list.filter { SESSION_RE.matches(it.name) }.forEach {
                    if (it.name !in tabs) {
                        tabs.add(it.name)
                        colorStore.get(server.id, it.name).takeIf { c -> c != 0 }?.let { c -> tabColors[it.name] = c }
                    }
                }
                error = null
            } catch (e: Exception) {
                error = e.message ?: e.javaClass.simpleName
            }
            if (tabs.isEmpty()) tabs.add("main")
            if (selected == null || selected !in tabs) selected = tabs.first()
        }
    }

    fun validName(name: String) = SESSION_RE.matches(name)

    /** Chip color for a tab, client-side only; 0 removes it. */
    fun setColor(name: String, argb: Int) {
        if (argb == 0) tabColors.remove(name) else tabColors[name] = argb
        colorStore.set(server.id, name, argb)
    }

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
        tabColors.remove(name) // session survives a detach; the store re-applies the color on reload
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
            tabColors[from]?.let { tabColors[to] = it }
            tabColors.remove(from)
            colorStore.rekey(server.id, from, to)
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
