package dev.muxalot.ui

import android.content.ClipboardManager
import android.content.Context
import android.net.ConnectivityManager
import android.net.Network
import androidx.activity.compose.BackHandler
import androidx.compose.foundation.background
import androidx.compose.foundation.gestures.detectTapGestures
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.BoxWithConstraints
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.LazyRow
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.lazy.rememberLazyListState
import androidx.compose.runtime.LaunchedEffect
import dev.muxalot.ui.kit.MuxChip
import dev.muxalot.ui.kit.MuxIcons
import dev.muxalot.ui.kit.RoundIconButton
import dev.muxalot.ui.kit.ScreenHeader
import dev.muxalot.ui.kit.StatusDot
import dev.muxalot.ui.kit.stateColor
import androidx.compose.foundation.layout.consumeWindowInsets
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.imePadding
import androidx.compose.foundation.layout.offset
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Scaffold
import androidx.compose.material3.ScrollableTabRow
import androidx.compose.material3.Tab
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.TopAppBar
import androidx.compose.runtime.Composable
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.key
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberUpdatedState
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.input.pointer.pointerInput
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import dev.muxalot.data.Server
import dev.muxalot.data.Shortcut
import dev.muxalot.data.ShortcutStore
import dev.muxalot.net.ConnState
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch

@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun TerminalScreen(server: Server, onFiles: () -> Unit, onShortcuts: () -> Unit, onBack: () -> Unit) {
    val ctx = LocalContext.current
    val scope = rememberCoroutineScope()
    val ctrl = remember(server.id) { TerminalController(server, scope) }
    var fontSize by rememberSaveable { mutableIntStateOf(14) }
    var menu by remember { mutableStateOf(false) }
    val shortcuts = remember { ShortcutStore(ctx).load() }
    var newTab by remember { mutableStateOf(false) }
    var closing by remember { mutableStateOf<String?>(null) }

    DisposableEffect(ctrl) {
        ctrl.load()
        val cm = ctx.getSystemService(Context.CONNECTIVITY_SERVICE) as ConnectivityManager
        val cb = object : ConnectivityManager.NetworkCallback() {
            override fun onAvailable(network: Network) = ctrl.kick()
        }
        cm.registerDefaultNetworkCallback(cb)
        onDispose {
            runCatching { cm.unregisterNetworkCallback(cb) }
            ctrl.dispose()
        }
    }
    BackHandler(onBack = onBack)

    Scaffold { pad ->
        Column(Modifier.padding(pad).consumeWindowInsets(pad).imePadding().fillMaxSize()) {
            ScreenHeader(
                title = server.name,
                onBack = onBack,
                leading = { ctrl.selected?.let { StatusDot(stateColor(ctrl.states[it])); Spacer(Modifier.width(8.dp)) } },
                actions = {
                    RoundIconButton(MuxIcons.Plus, "New tab") { newTab = true }
                    Box {
                        RoundIconButton(MuxIcons.More, "Menu") { menu = true }
                        DropdownMenu(expanded = menu, onDismissRequest = { menu = false }) {
                        val sel = ctrl.selected
                        DropdownMenuItem(text = { Text("Files") }, onClick = { menu = false; onFiles() })
                        DropdownMenuItem(text = { Text("Shortcuts") }, onClick = { menu = false; onShortcuts() })
                        DropdownMenuItem(text = { Text("Paste") }, onClick = {
                            menu = false
                            val cm = ctx.getSystemService(Context.CLIPBOARD_SERVICE) as ClipboardManager
                            val t = cm.primaryClip?.getItemAt(0)?.coerceToText(ctx)?.toString().orEmpty()
                            if (sel != null && t.isNotEmpty()) ctrl.enc(sel).paste(t)
                        })
                        DropdownMenuItem(text = { Text("Send clipboard to server (tmux buffer)") }, onClick = {
                            menu = false
                            val cm = ctx.getSystemService(Context.CLIPBOARD_SERVICE) as ClipboardManager
                            val t = cm.primaryClip?.getItemAt(0)?.coerceToText(ctx)?.toString().orEmpty()
                            if (sel != null && t.isNotEmpty()) ctrl.conn(sel).clipSet(t)
                        })
                        DropdownMenuItem(text = { Text("Copy server clipboard (tmux buffer)") }, onClick = {
                            menu = false
                            if (sel != null) ctrl.conn(sel).clipGet()
                        })
                        DropdownMenuItem(text = { Text("Font +") }, onClick = { fontSize = minOf(28, fontSize + 1) })
                        DropdownMenuItem(text = { Text("Font −") }, onClick = { fontSize = maxOf(8, fontSize - 1) })
                        DropdownMenuItem(text = { Text("Reconnect") }, onClick = { menu = false; ctrl.kick() })
                        DropdownMenuItem(text = { Text("Refresh sessions") }, onClick = { menu = false; ctrl.load() })
                        DropdownMenuItem(text = { Text("Close tab…") }, onClick = { menu = false; closing = sel })
                        }
                    }
                },
            )
            ctrl.error?.let {
                Text(it, color = MaterialTheme.colorScheme.error, fontSize = 12.sp, modifier = Modifier.padding(8.dp))
            }
            if (ctrl.tabs.isNotEmpty()) {
                val idx = ctrl.tabs.indexOf(ctrl.selected).coerceAtLeast(0)
                val tabState = rememberLazyListState()
                LaunchedEffect(idx) { tabState.animateScrollToItem(idx) }
                LazyRow(
                    state = tabState,
                    contentPadding = PaddingValues(horizontal = 16.dp, vertical = 8.dp),
                    horizontalArrangement = Arrangement.spacedBy(8.dp),
                ) {
                    items(ctrl.tabs, key = { it }) { name ->
                        val sel = name == ctrl.selected
                        // the selected tab's state is the dot in the header
                        MuxChip(name, sel, { ctrl.selected = name }, dot = if (sel) null else stateColor(ctrl.states[name]))
                    }
                }
            }
            Box(Modifier.weight(1f).fillMaxWidth()) {
                ctrl.tabs.forEach { name ->
                    key(name) {
                        TerminalPane(
                            conn = ctrl.conn(name),
                            enc = ctrl.enc(name),
                            visible = name == ctrl.selected,
                            fontSize = fontSize,
                            modifier = Modifier.fillMaxSize(),
                        )
                    }
                }
                if (ctrl.tabs.isEmpty()) {
                    Text("No sessions. Tap +.", color = MaterialTheme.colorScheme.onSurfaceVariant, modifier = Modifier.align(Alignment.Center))
                }
                ctrl.selected?.let { KeyFan(ctrl.enc(it), shortcuts, Modifier.fillMaxSize()) }
            }
        }
    }

    if (newTab) {
        var name by remember { mutableStateOf("") }
        AlertDialog(
            onDismissRequest = { newTab = false },
            title = { Text("New tmux session") },
            text = {
                OutlinedTextField(
                    value = name,
                    onValueChange = { name = it.trim() },
                    label = { Text("Name (letters, digits, - _)") },
                    singleLine = true,
                    isError = name.isNotEmpty() && !ctrl.validName(name),
                )
            },
            confirmButton = {
                TextButton(enabled = ctrl.validName(name), onClick = { ctrl.addTab(name); newTab = false }) { Text("Open") }
            },
            dismissButton = { TextButton(onClick = { newTab = false }) { Text("Cancel") } },
        )
    }

    closing?.let { name ->
        AlertDialog(
            onDismissRequest = { closing = null },
            title = { Text("Close “$name”") },
            text = { Text("Detach keeps the tmux session running on the server. Kill ends it and everything in it.") },
            confirmButton = { TextButton(onClick = { ctrl.closeTab(name, kill = true); closing = null }) { Text("Kill") } },
            dismissButton = {
                Row {
                    TextButton(onClick = { closing = null }) { Text("Cancel") }
                    TextButton(onClick = { ctrl.closeTab(name, kill = false); closing = null }) { Text("Detach") }
                }
            },
        )
    }
}

private class FanKey(
    val label: String, // short face text on the round button
    val name: String, // text label beside it
    val repeat: Boolean = false,
    val active: () -> Boolean = { false },
    val closes: Boolean = false, // close the fan after firing
    val act: () -> Unit,
)

private class FanGroup(val label: String, val name: String, val keys: List<FanKey>)

private const val BTN = 44 // dp, key button diameter
private const val STEP = 50 // dp, vertical spacing in a stack
private const val COL_W = 190 // dp, button + label
private const val EDGE = 8 // dp, gap to the screen corner
private const val MAIN = 48 // dp, fan button diameter

private fun fanGroups(enc: InputEncoder, shortcuts: List<Shortcut>): List<FanGroup> {
    fun k(label: String, name: String, f: () -> Unit) = FanKey(label, name, act = f)
    fun r(label: String, name: String, f: () -> Unit) = FanKey(label, name, repeat = true, act = f)
    fun ctl(letter: Char, name: String) = k("^$letter", name) { enc.raw((letter.code and 0x1f).toChar().toString()) }
    val fkeys = Key.entries.filter { it.name.length in 2..3 && it.name.startsWith("F") && it.name.drop(1).all(Char::isDigit) }
    return listOf(
        FanGroup("Mod", "Modifiers", listOf(
            k("Esc", "Escape") { enc.key(Key.ESC) },
            k("Tab", "Tab") { enc.key(Key.TAB) },
            FanKey("Ctrl", "Ctrl (sticky)", active = { enc.ctrl }) { enc.ctrl = !enc.ctrl },
            FanKey("Alt", "Alt (sticky)", active = { enc.alt }) { enc.alt = !enc.alt },
        )),
        FanGroup("↑↓", "Arrows", listOf(
            r("←", "Left") { enc.key(Key.LEFT) }, r("↓", "Down") { enc.key(Key.DOWN) },
            r("↑", "Up") { enc.key(Key.UP) }, r("→", "Right") { enc.key(Key.RIGHT) },
        )),
        FanGroup("^", "Ctrl combos", listOf(
            FanKey("Ctrl", "Ctrl (sticky)", active = { enc.ctrl }) { enc.ctrl = !enc.ctrl },
            FanKey("Alt", "Alt (sticky)", active = { enc.alt }) { enc.alt = !enc.alt },
            ctl('A', "Line start"), ctl('B', "tmux prefix"), ctl('C', "Interrupt"), ctl('D', "End of input"), ctl('E', "Line end"),
            ctl('K', "Kill to end"), ctl('L', "Clear screen"), ctl('R', "History search"),
            ctl('U', "Kill line"), ctl('W', "Delete word"), ctl('Z', "Suspend"),
        )),
        FanGroup("|~", "Symbols", listOf(
            "|" to "Pipe", "~" to "Tilde", "/" to "Slash", "-" to "Dash", "_" to "Underscore",
        ).map { (s, n) -> k(s, n) { enc.text(s) } }),
        FanGroup("Nav", "Navigation", listOf(
            k("Home", "Home") { enc.key(Key.HOME) }, k("End", "End") { enc.key(Key.END) },
            r("PgUp", "Page up") { enc.key(Key.PGUP) }, r("PgDn", "Page down") { enc.key(Key.PGDN) },
            k("Del", "Delete") { enc.key(Key.DELETE) },
        )),
        FanGroup("Fn", "F-keys", fkeys.map { f -> k(f.name, f.name) { enc.key(f) } }),
        FanGroup("🤖", "Claude", shortcuts.map { s ->
            FanKey(s.label, s.text, closes = true) { enc.text(s.text); if (s.enter) enc.key(Key.ENTER) }
        }),
    )
}

/**
 * Bottom-left button that stacks labelled shortcut keys upward: first groups, then the group's keys.
 * Stacks too tall for the screen continue in a column to the right. Stays open (sticky Ctrl/Alt,
 * held arrows) until the button or the dimmed terminal is tapped.
 */
@Composable
fun KeyFan(enc: InputEncoder, shortcuts: List<Shortcut>, modifier: Modifier = Modifier) {
    var open by remember { mutableStateOf(false) }
    var group by remember { mutableStateOf<FanGroup?>(null) }
    val groups = remember(enc, shortcuts) { fanGroups(enc, shortcuts) }
    fun close() { open = false; group = null }

    BoxWithConstraints(modifier) {
        if (open) {
            Box(Modifier.fillMaxSize().background(Color(0x66000000)).pointerInput(Unit) { detectTapGestures(onTap = { close() }) })
        }
        val g = group
        val items: List<FanKey> = if (open) g?.keys ?: groups.map { fg -> FanKey(fg.label, fg.name) { group = fg } } else emptyList()
        val perCol = ((maxHeight.value - EDGE - MAIN - EDGE) / STEP).toInt().coerceAtLeast(1)
        items.forEachIndexed { i, item ->
            val col = i / perCol
            val row = i % perCol
            FanRow(
                item,
                Modifier.align(Alignment.BottomStart).offset(
                    x = (EDGE + col * COL_W).dp,
                    y = -(EDGE + MAIN + EDGE + row * STEP).dp,
                ),
                if (item.closes) { { item.act(); close() } } else item.act,
            )
        }
        FanButton(
            if (!open) "⌨" else if (g != null) "‹" else "✕", !open && (enc.ctrl || enc.alt),
            Modifier.align(Alignment.BottomStart).padding(EDGE.dp)
                .fanPress(false) { if (!open) open = true else if (g != null) group = null else close() },
            size = MAIN,
        )
    }
}

/** Press fires [onPress] immediately; with [repeat] it keeps firing while held. */
@Composable
private fun Modifier.fanPress(repeat: Boolean, onPress: () -> Unit): Modifier {
    val scope = rememberCoroutineScope()
    val current by rememberUpdatedState(onPress)
    return pointerInput(repeat) {
        detectTapGestures(onPress = {
            current()
            if (!repeat) return@detectTapGestures
            val job = scope.launch {
                delay(400)
                while (true) {
                    current()
                    delay(60)
                }
            }
            tryAwaitRelease()
            job.cancel()
        })
    }
}

// The whole row (button and label) is the touch target; a label that ignores touches
// would fall through to the scrim and close the fan.
@Composable
private fun FanRow(item: FanKey, modifier: Modifier, onPress: () -> Unit) {
    Row(modifier.fanPress(item.repeat, onPress), verticalAlignment = Alignment.CenterVertically) {
        FanButton(item.label, item.active(), Modifier)
        Text(
            item.name,
            color = Color.White,
            fontSize = 14.sp,
            maxLines = 1,
            modifier = Modifier
                .padding(start = 6.dp)
                .clip(RoundedCornerShape(6.dp))
                .background(Color(0xE6151320))
                .padding(horizontal = 8.dp, vertical = 4.dp),
        )
    }
}

@Composable
private fun FanButton(label: String, active: Boolean, modifier: Modifier, size: Int = BTN) {
    Box(
        modifier
            .size(size.dp)
            .clip(CircleShape)
            .background(if (active) MaterialTheme.colorScheme.primary else Color(0xFF2A2536)),
        contentAlignment = Alignment.Center,
    ) {
        Text(label, color = if (active) MaterialTheme.colorScheme.onPrimary else Color.White, fontSize = if (size > BTN) 20.sp else 12.sp)
    }
}
