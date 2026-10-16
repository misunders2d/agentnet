package io.github.misunders2d.agentnet

import java.net.URI

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
