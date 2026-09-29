package dev.muxalot.net

import dev.muxalot.data.Server
import okhttp3.HttpUrl.Companion.toHttpUrl
import okhttp3.Request
import okhttp3.Response
import okhttp3.WebSocket
import okhttp3.WebSocketListener
import okio.ByteString
import okio.ByteString.Companion.toByteString
import org.json.JSONObject
import java.util.concurrent.Executors
import java.util.concurrent.TimeUnit

enum class ConnState { CONNECTING, CONNECTED, RECONNECTING, EXITED }

/**
 * One tmux session streamed over a WebSocket. Binary frames are raw terminal
 * bytes; text frames are JSON control messages (see agent/server.go).
 * Reconnects with backoff; because the session lives in tmux on the server,
 * reattaching restores the screen.
 *
 * Callbacks arrive on OkHttp threads.
 */
class TerminalConnection(private val server: Server, val session: String) {
    var onOpen: (() -> Unit)? = null
    var onData: ((ByteArray) -> Unit)? = null
    var onClip: ((String) -> Unit)? = null
    var onState: ((ConnState) -> Unit)? = null

    @Volatile var cols = 80
        private set
    @Volatile var rows = 24
        private set

    private val base = server.url.trimEnd('/').toHttpUrl()
    private val timer = Executors.newSingleThreadScheduledExecutor()
    private val lock = Any()
    private var ws: WebSocket? = null
    private var attempt = 0
    private var started = false
    private var closed = false

    fun start() {
        synchronized(lock) {
            if (started || closed) return
            started = true
        }
        connect()
    }

    private fun connect() {
        synchronized(lock) { if (closed) return }
        onState?.invoke(if (attempt == 0) ConnState.CONNECTING else ConnState.RECONNECTING)
        val url = base.newBuilder()
            .addPathSegment("ws")
            .addQueryParameter("session", session)
            .addQueryParameter("cols", cols.toString())
            .addQueryParameter("rows", rows.toString())
            .build()
        val req = Request.Builder().url(url)
            .header("Authorization", Auth.header(server, "GET", url)).build()
        val socket = Api.wsClient.newWebSocket(req, Listener())
        synchronized(lock) { ws = socket }
    }

    private inner class Listener : WebSocketListener() {
        private fun current(w: WebSocket) = synchronized(lock) { w === ws }

        override fun onOpen(webSocket: WebSocket, response: Response) {
            if (!current(webSocket)) return
            attempt = 0
            onState?.invoke(ConnState.CONNECTED)
            onOpen?.invoke()
        }

        override fun onMessage(webSocket: WebSocket, bytes: ByteString) {
            if (current(webSocket)) onData?.invoke(bytes.toByteArray())
        }

        override fun onMessage(webSocket: WebSocket, text: String) {
            if (!current(webSocket)) return
            val m = runCatching { JSONObject(text) }.getOrNull() ?: return
            when (m.optString("t")) {
                "clip" -> onClip?.invoke(m.optString("text"))
                "exit" -> {
                    synchronized(lock) { closed = true }
                    timer.shutdownNow()
                    onState?.invoke(ConnState.EXITED)
                    webSocket.close(1000, null)
                }
            }
        }

        override fun onClosed(webSocket: WebSocket, code: Int, reason: String) = lost(webSocket)
        override fun onFailure(webSocket: WebSocket, t: Throwable, response: Response?) = lost(webSocket)

        private fun lost(w: WebSocket) {
            if (!current(w)) return
            scheduleReconnect()
        }
    }

    private fun scheduleReconnect() {
        synchronized(lock) { if (closed) return }
        attempt++
        onState?.invoke(ConnState.RECONNECTING)
        val delay = minOf(15_000L, 500L shl minOf(attempt, 5))
        runCatching { timer.schedule({ connect() }, delay, TimeUnit.MILLISECONDS) }
    }

    /** Reconnect immediately (e.g. network changed). No-op before start(). */
    fun kick() {
        synchronized(lock) {
            if (!started || closed) return
            ws?.cancel()
            ws = null
        }
        attempt = 0
        connect()
    }

    fun send(data: ByteArray) {
        synchronized(lock) { ws }?.send(data.toByteString())
    }

    private fun sendCtl(json: JSONObject) {
        synchronized(lock) { ws }?.send(json.toString())
    }

    fun resize(c: Int, r: Int) {
        if (c <= 0 || r <= 0) return
        cols = c
        rows = r
        sendCtl(JSONObject().put("t", "resize").put("cols", c).put("rows", r))
    }

    /** Put text into the server's tmux paste buffer. */
    fun clipSet(text: String) = sendCtl(JSONObject().put("t", "clip_set").put("text", text))

    /** Ask for the tmux buffer; the reply arrives via [onClip]. */
    fun clipGet() = sendCtl(JSONObject().put("t", "clip_get"))

    /** Close the socket; the tmux session keeps running on the server. */
    fun close() {
        synchronized(lock) {
            closed = true
            ws?.close(1000, null)
            ws = null
        }
        onData = null
        onOpen = null
        onClip = null
        onState = null
        timer.shutdownNow()
    }
}
