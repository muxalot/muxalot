package dev.muxalot.ui

import android.annotation.SuppressLint
import android.content.ClipData
import android.content.ClipboardManager
import android.content.Context
import android.os.Handler
import android.os.Looper
import android.util.Base64
import android.view.View
import android.view.inputmethod.InputMethodManager
import android.webkit.JavascriptInterface
import android.webkit.WebView
import android.widget.FrameLayout
import android.widget.Toast
import androidx.compose.runtime.Composable
import androidx.compose.ui.Modifier
import androidx.compose.ui.viewinterop.AndroidView
import dev.muxalot.Edition
import dev.muxalot.data.ThemeStore
import dev.muxalot.net.TerminalConnection
import java.io.ByteArrayOutputStream

/**
 * xterm.js in a WebView for rendering; keyboard capture is native (KeyCaptureView).
 * The connection is started once the page reports its size, so the first
 * attach already uses the right cols/rows.
 */
@SuppressLint("SetJavaScriptEnabled", "ViewConstructor")
class TerminalPaneView(
    ctx: Context,
    private val conn: TerminalConnection,
    private val enc: InputEncoder,
) : FrameLayout(ctx) {
    private val web = WebView(ctx)
    val keys = KeyCaptureView(ctx, enc)
    private val main = Handler(Looper.getMainLooper())
    private val pending = ByteArrayOutputStream()
    private var flushQueued = false
    private var ready = false
    private var font = 14
    private var disposed = false

    init {
        web.settings.javaScriptEnabled = true
        web.settings.allowFileAccess = true // only bundled assets are loaded
        web.isFocusable = false
        web.isFocusableInTouchMode = false
        web.overScrollMode = OVER_SCROLL_NEVER
        web.isVerticalScrollBarEnabled = false
        web.setBackgroundColor(0xFF0B0F14.toInt())
        web.addJavascriptInterface(Bridge(), "Android")
        web.loadUrl("file:///android_asset/terminal.html")
        addView(web, LayoutParams(LayoutParams.MATCH_PARENT, LayoutParams.MATCH_PARENT))
        addView(keys, LayoutParams(1, 1))

        conn.onData = { bytes -> main.post { enqueue(bytes) } }
        conn.onOpen = {
            main.post {
                // Fresh attach: tmux repaints the whole screen, so start clean.
                pending.reset()
                js("tty.reset()")
            }
        }
        conn.onClip = { text -> main.post { copyToClipboard(text) } }
    }

    private fun js(code: String) {
        if (!disposed) web.evaluateJavascript(code, null)
    }

    private fun enqueue(bytes: ByteArray) {
        if (disposed) return
        pending.write(bytes)
        if (ready && !flushQueued) {
            flushQueued = true
            main.postDelayed({ flush() }, 8)
        }
    }

    private fun flush() {
        flushQueued = false
        if (!ready || disposed || pending.size() == 0) return
        val all = pending.toByteArray()
        pending.reset()
        var off = 0
        while (off < all.size) {
            val n = minOf(48 * 1024, all.size - off)
            val b64 = Base64.encodeToString(all, off, n, Base64.NO_WRAP)
            js("tty.write('$b64')")
            off += n
        }
    }

    fun copyToClipboard(text: String) {
        val cm = context.getSystemService(Context.CLIPBOARD_SERVICE) as ClipboardManager
        cm.setPrimaryClip(ClipData.newPlainText("terminal", text))
        Toast.makeText(context, "Copied", Toast.LENGTH_SHORT).show()
    }

    fun showKeyboard() {
        keys.requestFocus()
        (context.getSystemService(Context.INPUT_METHOD_SERVICE) as InputMethodManager)
            .showSoftInput(keys, InputMethodManager.SHOW_IMPLICIT)
    }

    fun setFontSize(px: Int) {
        if (px == font) return
        font = px
        if (ready) js("tty.setFont($px)")
    }

    fun setActive(active: Boolean) {
        visibility = if (active) View.VISIBLE else View.GONE
        if (active) {
            main.post {
                js("tty.fit()")
                keys.requestFocus()
            }
        }
    }

    fun dispose() {
        disposed = true
        conn.onData = null
        conn.onOpen = null
        conn.onClip = null
        web.removeJavascriptInterface("Android")
        web.destroy()
    }

    /** Called from the WebView's JS thread; hop to main where needed. */
    inner class Bridge {
        @JavascriptInterface
        fun onReady(cols: Int, rows: Int) {
            main.post {
                ready = true
                if (font != 14) js("tty.setFont($font)")
                if (Edition.isPro) js("tty.setTheme(${ThemeStore(context).themeJson()})")
                conn.resize(cols, rows)
                conn.start()
                flush()
            }
        }

        @JavascriptInterface
        fun onResize(cols: Int, rows: Int) {
            conn.resize(cols, rows)
        }

        @JavascriptInterface
        fun onModes(appCursor: Boolean, bracketed: Boolean, mouse: String) {
            enc.appCursor = appCursor
            enc.bracketed = bracketed
        }

        @JavascriptInterface
        fun onClipboard(text: String) {
            main.post { copyToClipboard(text) }
        }

        @JavascriptInterface
        fun onSelection(text: String) {
            main.post { copyToClipboard(text) }
        }

        @JavascriptInterface
        fun onTap() {
            main.post { showKeyboard() }
        }

        @JavascriptInterface
        fun onSend(s: String) {
            enc.raw(s)
        }
    }
}

@Composable
fun TerminalPane(
    conn: TerminalConnection,
    enc: InputEncoder,
    visible: Boolean,
    fontSize: Int,
    modifier: Modifier = Modifier,
) {
    AndroidView(
        modifier = modifier,
        factory = { ctx -> TerminalPaneView(ctx, conn, enc) },
        update = { v ->
            v.setFontSize(fontSize)
            v.setActive(visible)
        },
        onRelease = { it.dispose() },
    )
}
