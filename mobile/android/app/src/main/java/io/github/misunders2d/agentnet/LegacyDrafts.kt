package io.github.misunders2d.agentnet

/** Recovery exposes only bounded old composer entries and acknowledges exact values. */
object LegacyDrafts {
    private val keyPattern = Regex("(?:dm|thread):(?:[0-9a-f]{32}|[0-9a-f]{64})")
    fun valid(key: String, value: String): Boolean = keyPattern.matches(key) &&
        value.length <= 128 * 1024 && value.toByteArray(Charsets.UTF_8).size <= 128 * 1024
    fun acknowledged(key: String, requested: String, current: Any?): Boolean =
        valid(key, requested) && current is String && current == requested
    fun recover(entries: Map<String, *>): List<Pair<String, String>> {
        val result = mutableListOf<Pair<String, String>>()
        var bytes = 0
        for ((key, stored) in entries.toSortedMap()) {
            val value = stored as? String ?: continue
            if (!valid(key, value)) continue
            val size = value.toByteArray(Charsets.UTF_8).size
            if (result.size >= 256 || bytes + size > 1024 * 1024) continue
            result.add(key to value); bytes += size
        }
        return result
    }
}
