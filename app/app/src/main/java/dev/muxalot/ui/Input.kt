package dev.muxalot.ui

import android.content.Context
import android.text.InputType
import android.view.KeyEvent
import android.view.View
import android.view.inputmethod.BaseInputConnection
import android.view.inputmethod.EditorInfo
import android.view.inputmethod.InputConnection
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import dev.muxalot.net.TerminalConnection

enum class Key {
    ESC, TAB, BACKTAB, ENTER, BACKSPACE, DELETE, INSERT,
    UP, DOWN, LEFT, RIGHT, HOME, END, PGUP, PGDN,
    F1, F2, F3, F4, F5, F6, F7, F8, F9, F10, F11, F12
}

/**
 * Turns keys/text into terminal byte sequences for one connection.
 * Ctrl and Alt are one-shot "sticky" modifiers toggled from the extra-keys row.
 */
class InputEncoder(private val conn: TerminalConnection) {
    var ctrl by mutableStateOf(false)
    var alt by mutableStateOf(false)

    // Updated from xterm.js mode reports.
    @Volatile var appCursor = false
    @Volatile var bracketed = false

    fun raw(s: String) = conn.send(s.toByteArray(Charsets.UTF_8))
    fun raw(b: ByteArray) = conn.send(b)

    private fun ctrlCode(c: Char): Int? = when (c) {
        in 'a'..'z' -> c - 'a' + 1
        in 'A'..'Z' -> c - 'A' + 1
        '@', ' ' -> 0
        '[' -> 27
        '\\' -> 28
        ']' -> 29
        '^' -> 30
        '_' -> 31
        '?' -> 127
        else -> null
    }

    /** Typed text (from IME or hardware). Applies sticky Ctrl/Alt to the first char. */
    fun text(s: String, extraCtrl: Boolean = false, extraAlt: Boolean = false) {
        if (s.isEmpty()) return
        val useCtrl = ctrl || extraCtrl
        val useAlt = alt || extraAlt
        ctrl = false
        alt = false
        val first = s[0]
        val rest = s.substring(1)
        val out = StringBuilder()
        if (useAlt) out.append('\u001b')
        val code = if (useCtrl) ctrlCode(first) else null
        if (code != null) out.append(code.toChar()) else out.append(first)
        out.append(rest)
        raw(out.toString())
    }

    fun backspace(n: Int = 1) = raw("\u007f".repeat(n))

    fun key(k: Key, shift: Boolean = false) {
        val mod = 1 + (if (shift) 1 else 0) + (if (alt) 2 else 0) + (if (ctrl) 4 else 0)
        ctrl = false
        alt = false
        val csi = "\u001b["
        val ss3 = "\u001bO"
        fun arrow(c: Char) = if (mod > 1) "${csi}1;$mod$c" else if (appCursor) "$ss3$c" else "$csi$c"
        fun tilde(n: Int) = if (mod > 1) "$csi$n;$mod~" else "$csi$n~"
        fun fn(n: Int, ss3Char: Char?) =
            if (ss3Char != null) (if (mod > 1) "${csi}1;$mod$ss3Char" else "$ss3$ss3Char") else tilde(n)
        val s = when (k) {
            Key.ESC -> "\u001b"
            Key.TAB -> "\t"
            Key.BACKTAB -> "${csi}Z"
            Key.ENTER -> "\r"
            Key.BACKSPACE -> if (mod >= 3 && mod != 5) "\u001b\u007f" else "\u007f"
            Key.DELETE -> tilde(3)
            Key.INSERT -> tilde(2)
            Key.UP -> arrow('A')
            Key.DOWN -> arrow('B')
            Key.RIGHT -> arrow('C')
            Key.LEFT -> arrow('D')
            Key.HOME -> arrow('H')
            Key.END -> arrow('F')
            Key.PGUP -> tilde(5)
            Key.PGDN -> tilde(6)
            Key.F1 -> fn(11, 'P')
            Key.F2 -> fn(12, 'Q')
            Key.F3 -> fn(13, 'R')
            Key.F4 -> fn(14, 'S')
            Key.F5 -> fn(15, null)
            Key.F6 -> fn(17, null)
            Key.F7 -> fn(18, null)
            Key.F8 -> fn(19, null)
            Key.F9 -> fn(20, null)
            Key.F10 -> fn(21, null)
            Key.F11 -> fn(23, null)
            Key.F12 -> fn(24, null)
        }
        raw(s)
    }

    /** Paste text, using bracketed paste when the app inside the terminal enabled it. */
    fun paste(text: String) {
        val t = text.replace("\r\n", "\n").replace('\n', '\r')
        if (bracketed) raw("\u001b[200~$t\u001b[201~") else raw(t)
    }

    /** Hardware / IME key events. Returns true if handled. */
    fun keyEvent(e: KeyEvent): Boolean {
        if (e.action != KeyEvent.ACTION_DOWN) return false
        val shift = e.isShiftPressed
        val k = when (e.keyCode) {
            KeyEvent.KEYCODE_ENTER, KeyEvent.KEYCODE_NUMPAD_ENTER -> Key.ENTER
            KeyEvent.KEYCODE_DEL -> Key.BACKSPACE
            KeyEvent.KEYCODE_FORWARD_DEL -> Key.DELETE
            KeyEvent.KEYCODE_TAB -> if (shift) Key.BACKTAB else Key.TAB
            KeyEvent.KEYCODE_ESCAPE -> Key.ESC
            KeyEvent.KEYCODE_DPAD_UP -> Key.UP
            KeyEvent.KEYCODE_DPAD_DOWN -> Key.DOWN
            KeyEvent.KEYCODE_DPAD_LEFT -> Key.LEFT
            KeyEvent.KEYCODE_DPAD_RIGHT -> Key.RIGHT
            KeyEvent.KEYCODE_MOVE_HOME -> Key.HOME
            KeyEvent.KEYCODE_MOVE_END -> Key.END
            KeyEvent.KEYCODE_PAGE_UP -> Key.PGUP
            KeyEvent.KEYCODE_PAGE_DOWN -> Key.PGDN
            KeyEvent.KEYCODE_INSERT -> Key.INSERT
            in KeyEvent.KEYCODE_F1..KeyEvent.KEYCODE_F12 -> Key.entries[Key.F1.ordinal + (e.keyCode - KeyEvent.KEYCODE_F1)]
            else -> null
        }
        if (k != null) {
            if (e.isCtrlPressed) ctrl = true
            if (e.isAltPressed) alt = true
            key(k, shift)
            return true
        }
        // Printable: use the character without Ctrl applied so Ctrl+C -> 'c'.
        val ch = e.getUnicodeChar(e.metaState and KeyEvent.META_CTRL_MASK.inv())
        if (ch == 0 || ch and KeyCharacterMapCombiningAccent != 0) return false
        text(String(Character.toChars(ch)), extraCtrl = e.isCtrlPressed, extraAlt = e.isAltPressed)
        return true
    }

    private companion object {
        const val KeyCharacterMapCombiningAccent = 0x80000000.toInt()
    }
}

/**
 * Invisible focusable view that owns the soft keyboard. Input is sent straight
 * to the terminal, bypassing the WebView (whose IME handling is unreliable).
 * The IME is configured for raw text: no suggestions, no autocorrect.
 */
class KeyCaptureView(ctx: Context, private val enc: InputEncoder) : View(ctx) {
    private var composing = ""

    init {
        isFocusable = true
        isFocusableInTouchMode = true
    }

    override fun onCheckIsTextEditor() = true

    override fun onCreateInputConnection(outAttrs: EditorInfo): InputConnection {
        outAttrs.inputType = InputType.TYPE_CLASS_TEXT or
            InputType.TYPE_TEXT_VARIATION_VISIBLE_PASSWORD or
            InputType.TYPE_TEXT_FLAG_NO_SUGGESTIONS
        outAttrs.imeOptions = EditorInfo.IME_FLAG_NO_FULLSCREEN or
            EditorInfo.IME_FLAG_NO_EXTRACT_UI or EditorInfo.IME_ACTION_NONE
        composing = ""
        return object : BaseInputConnection(this, false) {
            /** Send only what changed versus the current composing text. */
            private fun applyComposing(new: String) {
                var i = 0
                while (i < composing.length && i < new.length && composing[i] == new[i]) i++
                if (composing.length > i) enc.backspace(composing.length - i)
                if (new.length > i) enc.text(new.substring(i))
                composing = new
            }

            override fun setComposingText(text: CharSequence, newCursorPosition: Int): Boolean {
                applyComposing(text.toString())
                return true
            }

            override fun commitText(text: CharSequence, newCursorPosition: Int): Boolean {
                applyComposing(text.toString())
                composing = ""
                return true
            }

            override fun finishComposingText(): Boolean {
                composing = ""
                return true
            }

            override fun deleteSurroundingText(beforeLength: Int, afterLength: Int): Boolean {
                if (composing.isNotEmpty()) composing = composing.dropLast(minOf(beforeLength, composing.length))
                if (beforeLength > 0) enc.backspace(beforeLength)
                if (afterLength > 0) enc.key(Key.DELETE) // forward delete from some IMEs
                return true
            }

            override fun sendKeyEvent(event: KeyEvent): Boolean = enc.keyEvent(event)

            override fun performEditorAction(actionCode: Int): Boolean {
                enc.key(Key.ENTER)
                return true
            }

            override fun getTextBeforeCursor(n: Int, flags: Int): CharSequence = ""
            override fun getTextAfterCursor(n: Int, flags: Int): CharSequence = ""
        }
    }

    override fun onKeyDown(keyCode: Int, event: KeyEvent): Boolean =
        enc.keyEvent(event) || super.onKeyDown(keyCode, event)
}
