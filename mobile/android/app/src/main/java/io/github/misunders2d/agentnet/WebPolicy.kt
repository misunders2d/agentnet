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
    fun bridge(source: String, mainFrame: Boolean) = mainFrame && internal(source) && runCatching { URI(source).rawPath.isNullOrEmpty() || URI(source).rawPath == "/" }.getOrDefault(false)
    companion object {
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
