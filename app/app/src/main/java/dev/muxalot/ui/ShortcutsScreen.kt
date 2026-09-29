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
import dev.muxalot.data.Shortcut
import dev.muxalot.data.ShortcutStore

private const val MAX_LABEL = 4 // fits the round fan button

/** Add, edit and delete the shortcuts in the fan's Claude group. */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun ShortcutsScreen(store: ShortcutStore, onBack: () -> Unit) {
    var list by remember { mutableStateOf(store.load()) }
    // null = closed; index -1 = adding
    var editing by remember { mutableStateOf<Int?>(null) }
    fun update(new: List<Shortcut>) { list = new; store.save(new) }
    BackHandler(onBack = onBack)

    Scaffold(
        topBar = {
            TopAppBar(
                title = { Text("Shortcuts") },
                navigationIcon = { TextButton(onClick = onBack) { Text("‹ Back") } },
                actions = { TextButton(onClick = { editing = -1 }) { Text("+ Add") } },
            )
        },
    ) { pad ->
        LazyColumn(Modifier.padding(pad).fillMaxSize()) {
            itemsIndexed(list) { i, s ->
                Row(
                    Modifier.fillMaxWidth().clickable { editing = i }.padding(16.dp),
                    horizontalArrangement = Arrangement.SpaceBetween,
                    verticalAlignment = Alignment.CenterVertically,
                ) {
                    Column(Modifier.weight(1f)) {
                        Text(s.text, style = MaterialTheme.typography.titleMedium, maxLines = 1, overflow = TextOverflow.Ellipsis)
                        Text(
                            "${s.label} · " + if (s.enter) "types and presses Enter" else "types only",
                            style = MaterialTheme.typography.bodySmall,
                        )
                    }
                    TextButton(onClick = { update(list.filterIndexed { j, _ -> j != i }) }) { Text("Delete") }
                }
                HorizontalDivider()
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
