package dev.muxalot.net

import android.util.Base64
import dev.muxalot.data.DeviceKey
import dev.muxalot.data.Server
import kotlinx.serialization.Serializable
import kotlinx.serialization.json.Json
import okhttp3.HttpUrl
import okhttp3.HttpUrl.Companion.toHttpUrl
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.RequestBody
import okhttp3.RequestBody.Companion.toRequestBody
import okhttp3.Response
import okio.BufferedSink
import org.json.JSONObject
import java.io.IOException
import java.io.InputStream
import java.io.OutputStream
import java.security.SecureRandom
import java.util.concurrent.TimeUnit

/**
 * Request signing. Header format (must match the agent's verify()):
 *   Authorization: Sig id="<device>", ts="<unix>", nonce="<b64url>", sig="<b64 DER>"
 * over "muxalot-v1\nMETHOD\nHOST\nREQUEST_URI\nts\nnonce".
 * The phone clock must be within 60s of the server's.
 */
object Auth {
    private val rnd = SecureRandom()

    fun header(server: Server, method: String, url: HttpUrl): String {
        val host = if (url.port == HttpUrl.defaultPort(url.scheme)) url.host else "${url.host}:${url.port}"
        val uri = url.encodedPath + (url.encodedQuery?.let { "?$it" } ?: "")
        val ts = (System.currentTimeMillis() / 1000).toString()
        val nonce = Base64.encodeToString(
            ByteArray(12).also { rnd.nextBytes(it) },
            Base64.URL_SAFE or Base64.NO_WRAP or Base64.NO_PADDING
        )
        val msg = "muxalot-v1\n$method\n$host\n$uri\n$ts\n$nonce".toByteArray()
        val sig = Base64.encodeToString(DeviceKey.sign(server.id, msg), Base64.NO_WRAP)
        return "Sig id=\"${server.deviceId}\", ts=\"$ts\", nonce=\"$nonce\", sig=\"$sig\""
    }
}

@Serializable
data class SessionInfo(val name: String, val windows: Int = 0, val attached: Int = 0, val created: Long = 0)

@Serializable
data class FileEntry(val name: String, val dir: Boolean, val size: Long = 0, val mtime: Long = 0)

@Serializable
data class LsResult(val path: String, val entries: List<FileEntry>)

/** Cap for text responses; a hostile server could otherwise stream us out of memory. Longer bodies are cut, so JSON parsing fails. */
private const val MAX_TEXT = 1 shl 20

private fun Response.text(): String = peekBody(MAX_TEXT.toLong()).string()

class ApiException(val code: Int, message: String) : IOException(message)

class Api(private val server: Server) {
    private val base = server.url.trimEnd('/').toHttpUrl()

    companion object {
        private val json = Json { ignoreUnknownKeys = true }
        val client: OkHttpClient = OkHttpClient.Builder()
            .followRedirects(false)
            .followSslRedirects(false)
            .connectTimeout(15, TimeUnit.SECONDS)
            .readTimeout(60, TimeUnit.SECONDS)
            .writeTimeout(60, TimeUnit.SECONDS)
            .build()
        val wsClient: OkHttpClient = client.newBuilder()
            .readTimeout(0, TimeUnit.MILLISECONDS)
            .pingInterval(20, TimeUnit.SECONDS)
            .build()

        /** Registers this device's public key using a one-time code. Returns the device id. */
        fun pair(baseUrl: String, code: String, name: String, pubKey: String): String {
            val url = baseUrl.trimEnd('/').toHttpUrl().newBuilder().addPathSegment("pair").build()
            val body = JSONObject().put("code", code).put("name", name).put("pubkey", pubKey)
                .toString().toRequestBody("application/json".toMediaType())
            client.newCall(Request.Builder().url(url).post(body).build()).execute().use { r ->
                val text = r.text()
                if (!r.isSuccessful) throw ApiException(r.code, text.trim().ifEmpty { "HTTP ${r.code}" })
                return JSONObject(text).getString("device_id")
            }
        }
    }

    fun url(vararg segs: String, query: Map<String, String> = emptyMap()): HttpUrl =
        base.newBuilder().apply {
            segs.forEach { addPathSegment(it) }
            query.forEach { (k, v) -> addQueryParameter(k, v) }
        }.build()

    private fun request(method: String, url: HttpUrl, body: RequestBody? = null): Request =
        Request.Builder().url(url).method(method, body)
            .header("Authorization", Auth.header(server, method, url)).build()

    private fun check(r: Response) {
        if (!r.isSuccessful) throw ApiException(r.code, r.text().trim().ifEmpty { "HTTP ${r.code}" })
    }

    fun sessions(): List<SessionInfo> =
        client.newCall(request("GET", url("sessions"))).execute().use { r ->
            check(r)
            json.decodeFromString(r.text())
        }

    fun kill(name: String) {
        client.newCall(request("DELETE", url("sessions", name))).execute().use { check(it) }
    }

    /** Renames the tmux session; the agent answers 409 when the new name is taken. */
    fun rename(from: String, to: String) {
        val body = """{"to":"$to"}""".toRequestBody("application/json".toMediaType())
        client.newCall(request("POST", url("sessions", from, "rename"), body)).execute().use { check(it) }
    }

    /** The agent's own update status (404 from an agent that predates self-update). */
    fun update(): UpdateInfo =
        client.newCall(request("GET", url("update"))).execute().use { r ->
            check(r)
            json.decodeFromString(r.text())
        }

    /** Asks the agent to install its latest release; 409 = nothing to do or already running, 501 = installer not re-run. */
    fun applyUpdate() {
        client.newCall(request("POST", url("update", "apply"), "".toRequestBody(null))).execute().use { check(it) }
    }

    fun ls(path: String): LsResult =
        client.newCall(request("GET", url("ls", query = mapOf("path" to path)))).execute().use { r ->
            check(r)
            json.decodeFromString(r.text())
        }

    /** [onProgress] gets (bytes so far, total or -1 if unknown). */
    fun download(path: String, out: OutputStream, onProgress: (Long, Long) -> Unit = { _, _ -> }) {
        client.newCall(request("GET", url("files", query = mapOf("path" to path)))).execute().use { r ->
            check(r)
            val total = r.body!!.contentLength()
            val buf = ByteArray(64 * 1024)
            var done = 0L
            r.body!!.byteStream().use { input ->
                while (true) {
                    val n = input.read(buf)
                    if (n < 0) break
                    out.write(buf, 0, n)
                    done += n
                    onProgress(done, total)
                }
            }
        }
    }

    /** Throws ApiException(409) if the file exists and [overwrite] is false. [onProgress] gets bytes sent so far. */
    fun upload(path: String, length: Long, overwrite: Boolean, onProgress: (Long) -> Unit = {}, open: () -> InputStream) {
        val body = object : RequestBody() {
            override fun contentType() = "application/octet-stream".toMediaType()
            override fun contentLength() = length
            override fun writeTo(sink: BufferedSink) {
                open().use { input ->
                    val buf = ByteArray(64 * 1024)
                    var sent = 0L
                    while (true) {
                        val n = input.read(buf)
                        if (n < 0) break
                        sink.write(buf, 0, n)
                        sent += n
                        onProgress(sent)
                    }
                }
            }
        }
        val q = mutableMapOf("path" to path)
        if (overwrite) q["overwrite"] = "1"
        val longClient = client.newBuilder().writeTimeout(0, TimeUnit.MILLISECONDS)
            .readTimeout(120, TimeUnit.SECONDS).build()
        longClient.newCall(request("PUT", url("files", query = q), body)).execute().use { check(it) }
    }
}

@Serializable
data class UpdateInfo(
    val current: String = "",
    val latest: String = "",
    val available: Boolean = false,
    val applying: Boolean = false,
    val last_error: String = "",
)

/** The newest stable GitHub release; the app only links to it (updates come from the release page, or Play for the pro edition). */
object Release {
    private fun parse(s: String): List<Int>? =
        s.removePrefix("v").split(".").takeIf { it.size == 3 }?.map { it.toIntOrNull() ?: return null }

    /** "dev" and "x.y.z-dev" builds never count as outdated. */
    fun newer(latest: String, cur: String): Boolean {
        val l = parse(latest) ?: return false
        val c = parse(cur) ?: return false
        for (i in 0..2) if (l[i] != c[i]) return l[i] > c[i]
        return false
    }

    /** Returns (tag, page url). */
    fun fetchLatest(): Pair<String, String> {
        val req = Request.Builder().url("https://api.github.com/repos/muxalot/muxalot/releases/latest")
            .header("Accept", "application/vnd.github+json").build()
        Api.client.newCall(req).execute().use { r ->
            if (!r.isSuccessful) throw ApiException(r.code, "GitHub: HTTP ${r.code}")
            val j = JSONObject(r.text())
            return j.getString("tag_name") to j.getString("html_url")
        }
    }
}
