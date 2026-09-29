package dev.muxalot.ui

import android.net.Uri
import android.os.Build
import android.provider.OpenableColumns
import androidx.activity.compose.BackHandler
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Button
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.LinearProgressIndicator
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.TopAppBar
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import com.journeyapps.barcodescanner.ScanContract
import com.journeyapps.barcodescanner.ScanOptions
import dev.muxalot.Edition
import dev.muxalot.data.DeviceKey
import dev.muxalot.data.Server
import dev.muxalot.data.ServerStore
import dev.muxalot.net.Api
import dev.muxalot.net.ApiException
import dev.muxalot.net.FileEntry
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import java.util.UUID

// ---------------------------------------------------------------- servers

@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun ServerListScreen(store: ServerStore, onOpen: (Server) -> Unit, onAdd: () -> Unit) {
    var servers by remember { mutableStateOf(store.load()) }
    var deleting by remember { mutableStateOf<Server?>(null) }
    Scaffold(
        topBar = { TopAppBar(title = { Text(if (Edition.isPro) "Servers · Supporter" else "Servers") }, actions = { TextButton(onClick = onAdd) { Text("+ Add") } }) },
    ) { pad ->
        if (servers.isEmpty()) {
            Column(Modifier.padding(pad).padding(24.dp).fillMaxSize(), verticalArrangement = Arrangement.Center) {
                Text("No servers yet.", style = MaterialTheme.typography.titleMedium)
                Text("On the server run:  muxalot-agent pair --url https://your.host\nthen scan the QR code.", modifier = Modifier.padding(top = 8.dp))
                Button(onClick = onAdd, modifier = Modifier.padding(top = 16.dp)) { Text("Pair a server") }
            }
        } else {
            LazyColumn(Modifier.padding(pad).fillMaxSize()) {
                items(servers, key = { it.id }) { s ->
                    Row(
                        Modifier.fillMaxWidth().clickable { onOpen(s) }.padding(16.dp),
                        horizontalArrangement = Arrangement.SpaceBetween,
                    ) {
                        Column(Modifier.weight(1f)) {
                            Text(s.name, style = MaterialTheme.typography.titleMedium, maxLines = 1, overflow = TextOverflow.Ellipsis)
                            Text(s.url, style = MaterialTheme.typography.bodySmall, maxLines = 1, overflow = TextOverflow.Ellipsis)
                        }
                        TextButton(onClick = { deleting = s }) { Text("Remove") }
                    }
                    HorizontalDivider()
                }
            }
        }
    }
    deleting?.let { s ->
        AlertDialog(
            onDismissRequest = { deleting = null },
            title = { Text("Remove ${s.name}?") },
            text = { Text("Deletes this phone's key for the server. Also run  muxalot-agent revoke ${s.deviceId}  on the server.") },
            confirmButton = {
                TextButton(onClick = { store.remove(s.id); servers = store.load(); deleting = null }) { Text("Remove") }
            },
            dismissButton = { TextButton(onClick = { deleting = null }) { Text("Cancel") } },
        )
    }
}

// ---------------------------------------------------------------- pairing

@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun PairScreen(store: ServerStore, initialUrl: String, initialCode: String, onDone: () -> Unit, onCancel: () -> Unit) {
    val scope = rememberCoroutineScope()
    var url by remember { mutableStateOf(initialUrl) }
    var code by remember { mutableStateOf(initialCode) }
    var name by remember { mutableStateOf(Build.MODEL ?: "phone") }
    var busy by remember { mutableStateOf(false) }
    var error by remember { mutableStateOf<String?>(null) }
    BackHandler(onBack = onCancel)

    val scan = rememberLauncherForActivityResult(ScanContract()) { res ->
        val uri = res.contents?.let { runCatching { Uri.parse(it) }.getOrNull() }
        if (uri != null && uri.scheme == "muxalot" && uri.host == "pair") {
            uri.getQueryParameter("url")?.let { url = it }
            uri.getQueryParameter("code")?.let { code = it }
        } else if (res.contents != null) error = "Not a pairing QR code"
    }

    fun pair() {
        val cleanUrl = url.trim().trimEnd('/')
        if (!cleanUrl.startsWith("https://")) { error = "URL must start with https://"; return }
        busy = true
        error = null
        scope.launch {
            val id = UUID.randomUUID().toString()
            try {
                val deviceId = withContext(Dispatchers.IO) {
                    val pub = DeviceKey.create(id) // private key stays in the Keystore
                    Api.pair(cleanUrl, code.trim(), name.trim(), pub)
                }
                val host = Uri.parse(cleanUrl).host ?: cleanUrl
                store.add(Server(id, host, cleanUrl, deviceId))
                onDone()
            } catch (e: Exception) {
                DeviceKey.delete(id)
                error = when {
                    e is ApiException && e.code == 401 -> "Invalid or expired code. Run  muxalot-agent pair  again."
                    e is ApiException && e.code == 429 -> "Too many attempts. Wait 15 minutes."
                    else -> e.message ?: e.javaClass.simpleName
                }
            } finally {
                busy = false
            }
        }
    }

    Scaffold(topBar = { TopAppBar(title = { Text("Pair server") }) }) { pad ->
        Column(Modifier.padding(pad).padding(16.dp).fillMaxSize(), verticalArrangement = Arrangement.spacedBy(12.dp)) {
            OutlinedButton(onClick = {
                scan.launch(ScanOptions().setDesiredBarcodeFormats(ScanOptions.QR_CODE).setPrompt("Scan the muxalot-agent pair QR").setBeepEnabled(false))
            }) { Text("Scan QR code") }
            OutlinedTextField(url, { url = it }, label = { Text("Server URL") }, singleLine = true, modifier = Modifier.fillMaxWidth())
            OutlinedTextField(code, { code = it }, label = { Text("Pairing code") }, singleLine = true, modifier = Modifier.fillMaxWidth())
            OutlinedTextField(name, { name = it }, label = { Text("This device's name") }, singleLine = true, modifier = Modifier.fillMaxWidth())
            error?.let { Text(it, color = MaterialTheme.colorScheme.error) }
            Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                Button(enabled = !busy && url.isNotBlank() && code.isNotBlank(), onClick = { pair() }) { Text("Pair") }
                TextButton(onClick = onCancel) { Text("Cancel") }
                if (busy) CircularProgressIndicator(Modifier.padding(start = 8.dp))
            }
            Text(
                "A key pair is generated on this phone. Only the public key is sent to the server; " +
                    "the private key never leaves the Android Keystore.",
                style = MaterialTheme.typography.bodySmall,
            )
        }
    }
}

// ---------------------------------------------------------------- files

@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun FilesScreen(server: Server, onBack: () -> Unit) {
    val ctx = LocalContext.current
    val scope = rememberCoroutineScope()
    val api = remember(server.id) { Api(server) }
    var path by remember { mutableStateOf(".") }
    var entries by remember { mutableStateOf<List<FileEntry>>(emptyList()) }
    var shown by remember { mutableStateOf("") }
    var busy by remember { mutableStateOf(false) }
    var status by remember { mutableStateOf<String?>(null) }
    var pendingDownload by remember { mutableStateOf<String?>(null) }
    var pendingUpload by remember { mutableStateOf<Triple<Uri, String, Long>?>(null) }
    var progress by remember { mutableStateOf<Float?>(null) } // pro: transfer progress 0..1
    BackHandler(onBack = onBack)

    fun track(done: Long, total: Long) {
        if (Edition.isPro && total > 0) progress = done.toFloat() / total
    }

    fun refresh() {
        scope.launch {
            busy = true
            try {
                val r = withContext(Dispatchers.IO) { api.ls(path) }
                entries = r.entries
                shown = r.path
                status = null
            } catch (e: Exception) {
                status = e.message
            } finally {
                busy = false
            }
        }
    }
    LaunchedEffect(path) { refresh() }

    fun join(dir: String, name: String) = if (dir.endsWith("/")) dir + name else "$dir/$name"

    val download = rememberLauncherForActivityResult(ActivityResultContracts.CreateDocument("application/octet-stream")) { uri ->
        val remote = pendingDownload
        pendingDownload = null
        if (uri != null && remote != null) scope.launch {
            busy = true
            status = "Downloading…"
            try {
                withContext(Dispatchers.IO) {
                    ctx.contentResolver.openOutputStream(uri)!!.use { api.download(remote, it, ::track) }
                }
                status = "Downloaded"
            } catch (e: Exception) {
                status = "Download failed: ${e.message}"
            } finally {
                busy = false
                progress = null
            }
        }
    }

    fun doUpload(uri: Uri, name: String, size: Long, overwrite: Boolean) {
        scope.launch {
            busy = true
            status = "Uploading $name…"
            try {
                withContext(Dispatchers.IO) {
                    api.upload(join(shown, name), size, overwrite, { track(it, size) }) { ctx.contentResolver.openInputStream(uri)!! }
                }
                status = "Uploaded $name"
                refresh()
            } catch (e: ApiException) {
                if (e.code == 409) pendingUpload = Triple(uri, name, size) else status = "Upload failed: ${e.message}"
            } catch (e: Exception) {
                status = "Upload failed: ${e.message}"
            } finally {
                busy = false
                progress = null
            }
        }
    }

    /** Display name and size (-1 if unknown) of a picked document. */
    fun meta(uri: Uri): Pair<String, Long> {
        var name = "upload"
        var size = -1L
        ctx.contentResolver.query(uri, null, null, null, null)?.use { c ->
            if (c.moveToFirst()) {
                c.getColumnIndex(OpenableColumns.DISPLAY_NAME).takeIf { it >= 0 }?.let { name = c.getString(it) }
                c.getColumnIndex(OpenableColumns.SIZE).takeIf { it >= 0 }?.let { size = c.getLong(it) }
            }
        }
        return name to size
    }

    val upload = rememberLauncherForActivityResult(ActivityResultContracts.OpenDocument()) { uri ->
        if (uri != null) {
            val (name, size) = meta(uri)
            doUpload(uri, name, size, overwrite = false)
        }
    }

    // pro: several files in one go, uploaded one after another; existing names are skipped
    val uploadMany = rememberLauncherForActivityResult(ActivityResultContracts.OpenMultipleDocuments()) { uris ->
        if (uris.isNotEmpty()) scope.launch {
            busy = true
            var ok = 0
            val skipped = mutableListOf<String>()
            var failure: String? = null
            for ((i, uri) in uris.withIndex()) {
                val (name, size) = meta(uri)
                status = "Uploading ${i + 1}/${uris.size}: $name"
                try {
                    withContext(Dispatchers.IO) {
                        api.upload(join(shown, name), size, false, { track(it, size) }) { ctx.contentResolver.openInputStream(uri)!! }
                    }
                    ok++
                } catch (e: ApiException) {
                    if (e.code == 409) skipped += name else { failure = "$name: ${e.message}"; break }
                } catch (e: Exception) {
                    failure = "$name: ${e.message}"
                    break
                }
            }
            progress = null
            busy = false
            status = buildString {
                append("Uploaded $ok of ${uris.size}")
                if (skipped.isNotEmpty()) append("; already exist: ${skipped.joinToString()}")
                failure?.let { append("; failed: $it") }
            }
            refresh()
        }
    }

    Scaffold(
        topBar = {
            TopAppBar(
                title = { Text("Files") },
                navigationIcon = { TextButton(onClick = onBack) { Text("‹ Back") } },
                actions = {
                    TextButton(onClick = { if (Edition.isPro) uploadMany.launch(arrayOf("*/*")) else upload.launch(arrayOf("*/*")) }) { Text("Upload") }
                },
            )
        },
    ) { pad ->
        Column(Modifier.padding(pad).fillMaxSize()) {
            Text(shown, style = MaterialTheme.typography.bodySmall, modifier = Modifier.padding(horizontal = 16.dp, vertical = 4.dp))
            val p = progress
            if (p != null) LinearProgressIndicator(progress = { p }, modifier = Modifier.fillMaxWidth().padding(horizontal = 16.dp))
            else if (busy) CircularProgressIndicator(Modifier.padding(horizontal = 16.dp))
            status?.let { Text(it, modifier = Modifier.padding(horizontal = 16.dp), style = MaterialTheme.typography.bodySmall) }
            LazyColumn(Modifier.weight(1f)) {
                item {
                    Text(
                        "..",
                        Modifier.fillMaxWidth().clickable { path = shown.substringBeforeLast('/', "/").ifEmpty { "/" } }.padding(16.dp),
                    )
                }
                items(entries, key = { it.name }) { e ->
                    Row(
                        Modifier.fillMaxWidth().clickable {
                            if (e.dir) path = join(shown, e.name)
                            else { pendingDownload = join(shown, e.name); download.launch(e.name) }
                        }.padding(16.dp),
                        horizontalArrangement = Arrangement.SpaceBetween,
                    ) {
                        Text(if (e.dir) "${e.name}/" else e.name, Modifier.weight(1f))
                        if (!e.dir) Text(humanSize(e.size), style = MaterialTheme.typography.bodySmall)
                    }
                }
            }
        }
    }

    pendingUpload?.let { (uri, name, size) ->
        AlertDialog(
            onDismissRequest = { pendingUpload = null },
            title = { Text("Overwrite $name?") },
            text = { Text("A file with that name already exists on the server.") },
            confirmButton = { TextButton(onClick = { pendingUpload = null; doUpload(uri, name, size, true) }) { Text("Overwrite") } },
            dismissButton = { TextButton(onClick = { pendingUpload = null }) { Text("Cancel") } },
        )
    }
}

private fun humanSize(n: Long): String = when {
    n < 1024 -> "$n B"
    n < 1024 * 1024 -> "%.1f KB".format(n / 1024.0)
    n < 1024L * 1024 * 1024 -> "%.1f MB".format(n / 1048576.0)
    else -> "%.2f GB".format(n / 1073741824.0)
}
