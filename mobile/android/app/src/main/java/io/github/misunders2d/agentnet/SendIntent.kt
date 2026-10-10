package io.github.misunders2d.agentnet

import org.json.JSONObject
import java.util.UUID

/** Persist the entire request before sending; retries cannot change its authority or target. */
object SendIntent {
    fun freeze(saved: JSONObject, chat: Chat, body: String, kind: String): JSONObject {
        if (saved.has("request")) return saved
        val id = UUID.randomUUID().toString().replace("-", "")
        val request = JSONObject().put("id", id).put("body", body)
        if (chat.conversation) request.put("conv", chat.id)
        else {
            request.put("to", chat.peer).put("kind", kind).put("reply_to", chat.id)
            if (chat.agentId.isNotBlank()) request.put("agent_id", chat.agentId)
        }
        return JSONObject().put("body", body).put("request", request)
    }

    /** Only proves this exact human send was stored locally, never delivery or execution. */
    fun isPersisted(saved: JSONObject, chat: Chat, thread: JSONObject): Boolean {
        val request = saved.optJSONObject("request") ?: return false
        val id = request.optString("id")
        if (!id.matches(Regex("[0-9a-f]{32}")) || request.opt("body") !is String ||
            thread.optString("id") != chat.id) return false
        // These are the complete requests this preview creates. Future fields
        // must acquire an explicit projection match rather than be ignored.
        val allowed = if (chat.conversation) setOf("id", "body", "conv")
            else setOf("id", "body", "to", "kind", "reply_to", "agent_id")
        if (request.keys().asSequence().any { it !in allowed }) return false
        val kind = if (chat.conversation) "message" else request.optString("kind").ifBlank { "message" }
        if (kind !in setOf("message", "question", "task")) return false
        val agentID = request.optString("agent_id")
        if (chat.conversation) {
            if (request.optString("conv") != chat.id) return false
        } else if (request.optString("to") != chat.peer ||
            thread.optString("peer") != chat.peer ||
            request.optString("reply_to") != chat.id || agentID != chat.agentId) return false

        return thread.optJSONArray("messages").objects().any { message ->
            val storedID = if (chat.conversation) message.optString("lid").ifBlank { message.optString("id") }
                else message.optString("id")
            val target = message.optJSONObject("target")
            // agent_id on a message is its named author; the selected executor
            // of this human request is target.agent_id in the Go projection.
            val targetMatches = if (chat.conversation) target == null
                else if (agentID.isBlank()) target == null
                else target != null && target.optString("address") == request.optString("to") &&
                    target.optString("agent_id") == agentID
            storedID == id && message.optString("dir") == "out" &&
                message.optString("kind") == kind &&
                // Controls leave body as originally admitted. Never compare
                // edited text, deletion labels, previews or receipt words.
                message.optString("body") == request.getString("body").trim() &&
                message.optString("reply_to") == request.optString("reply_to") &&
                message.optString("quote").isBlank() &&
                (message.optJSONArray("files")?.length() ?: 0) == 0 &&
                (message.optJSONArray("attachments")?.length() ?: 0) == 0 &&
                message.optString("agent_id").isBlank() &&
                !message.optBoolean("verified_agent") &&
                message.optString("agent_author_pid").isBlank() && targetMatches &&
                (chat.conversation || message.optString("to") == request.optString("to"))
        }
    }

    fun acknowledged(saved: JSONObject, id: String) = saved.optJSONObject("request")?.optString("id") == id
}
