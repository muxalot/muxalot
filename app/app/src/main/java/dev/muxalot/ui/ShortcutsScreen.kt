package dev.muxalot.ui

import androidx.activity.compose.BackHandler
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.itemsIndexed
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Switch
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.TopAppBar
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.material3.OutlinedButton
import androidx.compose.ui.platform.LocalContext
import dev.muxalot.Edition
import dev.muxalot.data.Shortcut
import dev.muxalot.data.ShortcutStore
import dev.muxalot.data.ThemeStore
import dev.muxalot.data.Themes
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.lazy.LazyRow
import androidx.compose.foundation.lazy.items
import dev.muxalot.ui.kit.MuxCard
import dev.muxalot.ui.kit.MuxChip
import dev.muxalot.ui.kit.MuxIcons
import dev.muxalot.ui.kit.RoundIconButton
import dev.muxalot.ui.kit.ScreenHeader

private const val MAX_LABEL = 4 // fits the round fan button

/** Add, edit and delete the shortcuts in the Claude group. */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun ShortcutsScreen(store: ShortcutStore, onBack: () -> Unit) {
    var list by remember { mutableStateOf(store.load()) }
    // null = closed; index -1 = adding
    var editing by remember { mutableStateOf<Int?>(null) }
    fun update(new: List<Shortcut>) { list = new; store.save(new) }
    BackHandler(onBack = onBack)

    // pro: shortcut backup and terminal themes
    val ctx = LocalContext.current
    val themes = remember { ThemeStore(ctx) }
    var theme by remember { mutableStateOf(themes.name()) }
    var note by remember { mutableStateOf<String?>(null) }
    val export = rememberLauncherForActivityResult(ActivityResultContracts.CreateDocument("application/json")) { uri ->
        if (uri != null) note = runCatching {
            ctx.contentResolver.openOutputStream(uri)!!.use { it.write(store.toJson(list).toByteArray()) }
            "Exported ${list.size} shortcuts"
        }.getOrElse { "Export failed: ${it.message}" }
    }
    val import = rememberLauncherForActivityResult(ActivityResultContracts.OpenDocument()) { uri ->
        if (uri != null) note = runCatching {
            val text = ctx.contentResolver.openInputStream(uri)!!.use { it.readBytes().decodeToString() }
            val valid = store.parse(text).filter { it.text.isNotEmpty() && it.label.isNotEmpty() }
                .map { it.copy(label = it.label.take(MAX_LABEL)) }
            update(valid)
            "Imported ${valid.size} shortcuts"
        }.getOrElse { "Import failed: ${it.message}" }
    }

    Scaffold { pad ->
        Column(Modifier.padding(pad).fillMaxSize()) {
            ScreenHeader("Shortcuts", onBack = onBack, actions = { RoundIconButton(MuxIcons.Plus, "Add shortcut") { editing = -1 } })
            LazyColumn(Modifier.weight(1f), contentPadding = PaddingValues(16.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
                if (Edition.isPro) item {
                    MuxCard {
                        Column(Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(12.dp)) {
                            Text("Backup", style = MaterialTheme.typography.titleMedium)
                            Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                                OutlinedButton(onClick = { export.launch("muxalot-shortcuts.json") }) { Text("Export") }
                                OutlinedButton(onClick = { import.launch(arrayOf("application/json", "text/plain", "*/*")) }) { Text("Import (replaces list)") }
                            }
                            note?.let { Text(it, style = MaterialTheme.typography.bodySmall) }
                            Text("Terminal theme", style = MaterialTheme.typography.titleMedium)
                            LazyRow(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                                items(Themes.presets.keys.toList()) { n ->
                                    MuxChip(n, selected = n == theme, onClick = { themes.setName(n); theme = n })
                                }
                            }
                        }
                    }
                }
                itemsIndexed(list) { i, s ->
                    MuxCard(onClick = { editing = i }) {
                        Row(Modifier.padding(16.dp), verticalAlignment = Alignment.CenterVertically) {
                            Column(Modifier.weight(1f)) {
                                Text(s.text, style = MaterialTheme.typography.titleMedium, maxLines = 1, overflow = TextOverflow.Ellipsis)
                                Text(
                                    "${s.label} · " + if (s.enter) "types and presses Enter" else "types only",
                                    style = MaterialTheme.typography.bodySmall,
                                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                                )
                            }
                            TextButton(onClick = { update(list.filterIndexed { j, _ -> j != i }) }) { Text("Delete") }
                        }
                    }
                }
            }
        }
    }

    editing?.let { idx ->
        val cur = list.getOrNull(idx)
        var label by remember { mutableStateOf(cur?.label.orEmpty()) }
        var text by remember { mutableStateOf(cur?.text.orEmpty()) }
        var enter by remember { mutableStateOf(cur?.enter ?: true) }
        AlertDialog(
            onDismissRequest = { editing = null },
            title = { Text(if (cur == null) "New shortcut" else "Edit shortcut") },
            text = {
                Column(verticalArrangement = Arrangement.spacedBy(8.dp)) {
                    OutlinedTextField(text, { text = it }, label = { Text("Text to type") }, singleLine = true)
                    OutlinedTextField(label, { label = it.take(MAX_LABEL) }, label = { Text("Button label (max $MAX_LABEL)") }, singleLine = true)
                    Row(verticalAlignment = Alignment.CenterVertically) {
                        Text("Press Enter", Modifier.weight(1f))
                        Switch(enter, { enter = it })
                    }
                }
            },
            confirmButton = {
                TextButton(enabled = text.isNotEmpty() && label.isNotEmpty(), onClick = {
                    val s = Shortcut(label, text, enter)
                    update(if (cur == null) list + s else list.mapIndexed { j, o -> if (j == idx) s else o })
                    editing = null
                }) { Text("Save") }
            },
            dismissButton = { TextButton(onClick = { editing = null }) { Text("Cancel") } },
        )
    }
}
