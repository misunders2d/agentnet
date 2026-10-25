package io.github.misunders2d.agentnet

import java.net.URI
import org.json.JSONArray
import org.json.JSONObject

/** Only the assigned loopback origin may run inside the app or call native code. */
class WebPolicy(val origin: String) {
    private val own = URI(origin)
    init { require(own.scheme == "http" && own.host == "127.0.0.1" && own.port in 1..65535 && own.rawUserInfo == null && own.rawPath.isNullOrEmpty()) }
    fun internal(url: String): Boolean = runCatching {
        val u = URI(url)
        u.scheme == own.scheme && u.host == own.host && u.port == own.port && u.rawUserInfo == null
    }.getOrDefault(false)
    fun external(url: String): Boolean = runCatching {
        val u = URI(url)
        u.scheme == "https" && !u.host.isNullOrBlank() && u.rawUserInfo == null && u.host != "127.0.0.1" && u.host != "localhost"
    }.getOrDefault(false)
    /** Read-only gallery subresources; navigation and native bridge remain loopback-only. */
    fun gallery(url: String, method: String, mainFrame: Boolean): Boolean = !mainFrame && method == "GET" && runCatching {
        val u = URI(url)
        u.scheme == "https" && u.rawUserInfo == null && u.port in setOf(-1, 443) &&
            u.rawQuery == null && u.rawFragment == null && when (u.host) {
                "api.github.com" -> u.rawPath == "/repos/misunders2d/agentnet-skins/releases/latest"
                "raw.githubusercontent.com" -> u.rawPath.startsWith("/misunders2d/agentnet-skins/") &&
                    u.rawPath.split('/').drop(1).all { it.matches(Regex("[A-Za-z0-9][A-Za-z0-9._-]{0,99}")) }
                else -> false
            }
    }.getOrDefault(false)
    fun bridge(source: String, mainFrame: Boolean) = mainFrame && internal(source) && runCatching { URI(source).rawPath.isNullOrEmpty() || URI(source).rawPath == "/" }.getOrDefault(false)
    companion object {
        /** A portable link only supplies validated identifiers to the trusted
         * local page. It never supplies a URL for WebView navigation. */
        fun externalLink(value: String): String? = runCatching {
            require(value.length <= 256)
            val uri = URI(value)
            require(uri.scheme == "agentnet" && uri.rawAuthority == "open" && uri.rawPath.isNullOrEmpty() && uri.rawQuery == null)
            val fragment = uri.rawFragment ?: error("missing destination")
            require(fragment.matches(Regex("[a-z0-9=&]+")))
            val fields = linkedMapOf<String,String>()
            fragment.split('&').forEach { part ->
                val pieces = part.split('=')
                require(pieces.size == 2 && pieces[0] in setOf("realm","conv","topic","thread") && !fields.containsKey(pieces[0]))
                fields[pieces[0]] = pieces[1]
            }
            val id = Regex("[0-9a-f]{32}")
            require(fields["realm"]?.matches(id) == true)
            if(fields.containsKey("thread"))require(fields.keys == setOf("realm","thread") && fields["thread"]?.matches(id) == true)
            else require(fields.keys == setOf("realm","conv","topic") && fields["conv"]?.matches(Regex("[0-9a-f]{64}")) == true && (fields["topic"] == "main" || fields["topic"]?.matches(id) == true))
            fragment
        }.getOrNull()
        /** Scanner output is untrusted text, never a URL to open or enrollment authority. */
        fun scanText(text: String): String {
            require(text.isNotBlank() && text.length <= 16 * 1024 && text.toByteArray(Charsets.UTF_8).size <= 16 * 1024)
            return text
        }
        /** Enriching a click destination must not change the notification that
         *  an exact message read dismisses. Keep the legacy message tag. */
        fun notificationTag(workspace: String, fragment: String): String? {
            val route = notificationDestination(workspace, fragment) ?: return null
            return if (route.startsWith("msg=")) route.split('&').filterNot { it.startsWith("conv=") }.joinToString("&") else route
        }
        /** Bind a validated content-free destination to its exact membership. */
        fun notificationDestination(workspace: String, fragment: String): String? = runCatching {
            require(workspace == "default" || workspace.matches(Regex("[0-9a-f]{32}")))
            require(fragment.length <= 2048 && !fragment.contains('#'))
            if (fragment.isEmpty()) return@runCatching "workspace=" + workspace
            val fields = linkedMapOf<String, String>()
            fragment.split('&').forEach { item ->
                val key = java.net.URLDecoder.decode(item.substringBefore('='), "UTF-8")
                val value = if ('=' in item) java.net.URLDecoder.decode(item.substringAfter('='), "UTF-8") else ""
                require(key in setOf("review", "conv", "msg", "dir", "workspace") && !fields.containsKey(key))
                fields[key] = value
            }
            require(fields["workspace"] == null || fields["workspace"] == workspace)
            fields["conv"]?.let { require(it.matches(Regex("[0-9a-f]{64}"))) }
            fields["msg"]?.let { require(it.matches(Regex("[0-9a-f]{32}"))) }
            fields["dir"]?.let { require(fields.containsKey("msg") && it in setOf("in", "out")) }
            if (fields.keys == setOf("workspace")) return@runCatching "workspace=" + workspace
            val primary = when {
                fields.containsKey("review") -> { require(fields["review"] == "" && fields.keys.all { it in setOf("review", "workspace") }); "review" }
                fields.containsKey("msg") -> "msg=" + fields.getValue("msg") + (fields["conv"]?.let { "&conv=$it" } ?: "") + (fields["dir"]?.let { "&dir=$it" } ?: "")
                else -> "conv=" + fields.getValue("conv")
            }
            primary + "&workspace=" + workspace
        }.getOrNull()
    }
}

internal const val NOTICE_METADATA_EXTRA = "io.github.misunders2d.agentnet.notice.v1"
internal const val NOTICE_METADATA_MAX = 32768
internal const val NOTICE_RESTORE_CHUNK = 32

// Content-free host metadata only. Go remains the unread/reminder/Review engine.
internal data class ActivityNoticeRecord(val workspace: String, val tag: String, val metadata: String, val instance: String)

internal fun activityNoticeRecord(ownPackage: String, packageName: String, channel: String, id: Int,
    tag: String?, metadata: String?): ActivityNoticeRecord? = runCatching {
    require(packageName == ownPackage && channel == ACTIVITY_CHANNEL && id == 0)
    require(tag != null && tag.length <= 2048)
    val workspace = tag.split('&').single { it.startsWith("workspace=") }.substringAfter('=')
    require(WebPolicy.notificationTag(workspace, tag) == tag)
    if (metadata == null) return@runCatching ActivityNoticeRecord(workspace, tag, "", "")
    require(metadata.toByteArray(Charsets.UTF_8).size <= NOTICE_METADATA_MAX)
    val value = JSONObject(metadata)
    require(value.keys().asSequence().toSet().all { it in setOf("v", "workspace", "tag", "instance", "category", "ref", "reminders") })
    require(value.get("v") == 1 && value.get("workspace") == workspace && value.get("tag") == tag)
    val hexID = Regex("[0-9a-f]{32}")
    val hash = Regex("[0-9a-f]{64}")
    val fingerprint = Regex("[0-9a-f]{8}(-[0-9a-f]{8}){3}")
    val instance = value.get("instance") as String
    require(instance.matches(hexID))
    val category = value.get("category") as String
    val ref = if (value.has("ref")) value.getJSONObject("ref") else null
    if (ref != null) {
        require(ref.keys().asSequence().toSet() == setOf("conv", "fingerprint", "lid"))
        require((ref.get("conv") as String).let { it.isEmpty() || it.matches(hash) })
        require((ref.get("fingerprint") as String).matches(fingerprint) && (ref.get("lid") as String).matches(hexID))
    }
    val reminders = if (value.has("reminders")) value.getJSONArray("reminders") else JSONArray()
    require(reminders.length() <= 256)
    val seen = mutableSetOf<String>()
    for (index in 0 until reminders.length()) {
        val reminder = reminders.getJSONObject(index)
        require(reminder.keys().asSequence().toSet() == setOf("message", "revision"))
        val message = reminder.get("message") as String
        val revision = reminder.get("revision")
        require(message.matches(hexID) && seen.add(message) && (revision is Int || revision is Long))
        require((revision as Number).toLong() in 1..9007199254740991L)
    }
    require(when(category) {
        "activity" -> ref != null && reminders.length() == 0
        "reminder" -> reminders.length() > 0
        "reminder-summary" -> ref == null && reminders.length() == 0
        "decision", "security", "unknown" -> reminders.length() == 0
        else -> false
    })
    ActivityNoticeRecord(workspace, tag, metadata, instance)
}.getOrNull()

internal fun activityRestoreChunks(records: List<ActivityNoticeRecord>, unknown: Int, known: Boolean = true): List<String> {
    require(unknown in 0..1000000 && (known || records.isEmpty()))
    val chunks = if (records.isEmpty()) listOf(emptyList()) else records.chunked(NOTICE_RESTORE_CHUNK)
    return chunks.mapIndexed { index, chunk -> JSONObject().put("v", 1).put("known", known)
        .put("unknown", if(index == 0) unknown else 0).put("entries", JSONArray().apply {
            chunk.forEach { record -> put(JSONObject().put("workspace", record.workspace).put("tag", record.tag).put("metadata", record.metadata)) }
        }).toString() }
}

// The main lane's latest emission also fences an OS snapshot still showing the
// older object after notify(). Never cancel/repost that stale snapshot.
internal fun activityInstanceMatches(record: ActivityNoticeRecord, expected: String, latest: Map<String, String>): Boolean =
    record.instance == expected && (!latest.containsKey(record.tag) || latest[record.tag] == expected)

internal fun activityBadgeEligible(record: ActivityNoticeRecord, dismissed: Map<String, String>, latest: Map<String, String>): Boolean =
    dismissed[record.tag] != record.instance && activityInstanceMatches(record, record.instance, latest)
