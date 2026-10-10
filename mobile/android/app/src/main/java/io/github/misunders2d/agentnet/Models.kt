package io.github.misunders2d.agentnet

import org.json.JSONArray
import org.json.JSONObject
import java.time.Instant
import java.time.OffsetDateTime

data class Chat(val id: String, val title: String, val preview: String, val at: String,
                val unread: Int, val conversation: Boolean, val peer: String = "", val agentId: String = "") {
    val key get() = (if (conversation) "dm:" else "thread:") + id
}

data class ChatMessage(val id: String, val body: String, val author: String, val at: String,
                       val outgoing: Boolean, val state: String, val unread: Boolean)

object Models {
    fun chats(overview: JSONObject): List<Chat> {
        val dms = overview.optJSONArray("dms").objects().map {
            Chat(it.getString("id"), (if (it.optString("kind") == "group") it.optString("title")
                else it.optJSONObject("peer")?.optString("label").orEmpty())
                .ifBlank { it.optString("title").ifBlank { "Conversation" } },
                it.optString("last"), it.optString("last_at"), it.optInt("unread"), true)
        }
        val threads = overview.optJSONArray("threads").objects().filter {
            !it.optBoolean("notice_only") && it.optString("conv").isBlank()
        }.map {
            Chat(it.getString("id"), it.optString("title").ifBlank { it.optString("peer") },
                it.optString("last"), it.optString("last_at"), it.optInt("unread"), false,
                it.optString("peer"), it.optString("agent_id"))
        }
        return (dms + threads).sortedByDescending { timestamp(it.at) }
    }

    // Go's RFC3339Nano timestamps can mix fractions and timezone offsets.
    // Comparing their text puts 10:00:00Z ahead of the later 10:00:00.5Z.
    private fun timestamp(value: String): Instant = runCatching {
        OffsetDateTime.parse(value).toInstant()
    }.getOrDefault(Instant.MIN)

    fun messages(thread: JSONObject, chat: Chat): List<ChatMessage> {
        val agents = thread.optJSONArray("agents").objects().associateBy { it.optString("pid") }
        // Preserve every row and the core's reply/event ordering. ListView
        // virtualizes presentation; a future page API can bound core reads.
        return thread.optJSONArray("messages").objects().map { m ->
            val outgoing = m.optString("dir") == "out"
            val body = when {
                m.optBoolean("deleted") -> "Message deleted"
                m.optBoolean("edited") -> m.optString("text")
                m.optString("event").isNotBlank() -> m.optString("event")
                else -> m.optString("body")
            }
            val author = author(m, thread, chat, agents)
            val controls = when {
                m.optBoolean("deleted") -> "Deleted"
                m.optBoolean("edited") -> "Edited"
                else -> ""
            }
            // Receipts and execution outcomes remain the core's own words.
            val state = listOf(m.optString("state_text"), controls)
                .filter { it.isNotBlank() }.joinToString(" · ")
            ChatMessage(m.getString("id"), body, author, m.optString("at"), outgoing,
                state, m.optBoolean("unread"))
        }
    }

    private fun author(m: JSONObject, thread: JSONObject, chat: Chat,
                       agents: Map<String, JSONObject>): String {
        val from = m.optString("from")
        val history = m.optString("synced_from")
        val excerpt = m.optString("excerpt_pid").isNotBlank()
        val claimed = m.optString("claimed_key").isNotBlank()
        if (excerpt || (claimed && history.isNotBlank())) {
            return "Claimed ${from.ifBlank { "author" }} · forwarded " + if (excerpt) "context" else "history"
        }
        val label = when {
            // Direction does not prove a human author. Own agent turns copied
            // to this phone can have dir=out and still remain agent turns.
            m.optBoolean("verified_agent") -> {
                val pid = m.optString("agent_author_pid").ifBlank { m.optString("pid") }
                val agent = agents[pid]?.takeIf {
                    it.optJSONObject("host")?.optString("address") == from
                }
                val host = agent?.optJSONObject("host")
                val hostName = host?.optString("label").orEmpty().ifBlank { from }
                val agentID = m.optString("agent_id").ifBlank { agent?.optString("agent_id").orEmpty() }
                val named = if (agentID.isBlank()) "Agent" else "Agent $agentID"
                "$named on ${hostName.ifBlank { "unknown host" }}" +
                    if (m.optBoolean("edited")) " · edited by a person" else ""
            }
            // Device-thread author labels are already projected by Go. Do
            // not substitute You for a named executor or uncertain writer.
            m.optJSONObject("author")?.optString("label").orEmpty().isNotBlank() ->
                m.getJSONObject("author").getString("label")
            m.optString("event_by").isNotBlank() -> m.optString("event_by")
            m.optString("dir") == "out" -> "You" + m.optString("via")
                .takeIf { it.isNotBlank() }?.let { " · $it" }.orEmpty()
            else -> {
                val guest = thread.optJSONArray("guests").objects().mapNotNull { it.optJSONObject("host") }
                    .firstOrNull { personHasDevice(it, from) }
                val member = thread.optJSONArray("members").objects().firstOrNull { personHasDevice(it, from) }
                val peer = thread.optJSONObject("peer")?.takeIf { personHasDevice(it, from) }
                (guest ?: member ?: peer)?.optString("label").orEmpty().ifBlank {
                    from.ifBlank { chat.title }
                }
            }
        }
        return label + if (history.isNotBlank()) " · history from $history (author not checked here)" else ""
    }

    private fun personHasDevice(person: JSONObject, address: String): Boolean = address.isNotBlank() &&
        (person.optString("address") == address ||
            person.optJSONArray("devices").objects().any { it.optString("address") == address })
}

internal fun JSONArray?.objects(): List<JSONObject> = if (this == null) emptyList()
    else (0 until length()).mapNotNull { optJSONObject(it) }
