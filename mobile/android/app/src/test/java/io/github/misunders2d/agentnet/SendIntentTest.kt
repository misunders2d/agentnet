package io.github.misunders2d.agentnet

import org.json.JSONObject
import org.junit.Assert.*
import org.junit.Test

class SendIntentTest {
    private val chat = Chat("root-id", "Agent", "", "", 0, false, "owner/laptop", "codex-id")
    @Test fun retryAfterRestartRetainsAllOriginalBytesAndIntent() {
        val first = SendIntent.freeze(JSONObject(), chat, "Check this", "question")
        val restored = JSONObject(first.toString())
        val retry = SendIntent.freeze(restored, chat.copy(peer = "someone/else"), "Changed text", "task")
        assertEquals(first.toString(), retry.toString())
        assertEquals("question", retry.getJSONObject("request").getString("kind"))
        assertEquals("codex-id", retry.getJSONObject("request").getString("agent_id"))
        assertEquals("root-id", retry.getJSONObject("request").getString("reply_to"))
    }
    @Test fun acknowledgementNeverClearsAnotherDraft() {
        val first = SendIntent.freeze(JSONObject(), chat, "one", "message")
        val second = SendIntent.freeze(JSONObject(), chat, "two", "message")
        assertTrue(SendIntent.acknowledged(first, first.getJSONObject("request").getString("id")))
        assertFalse(SendIntent.acknowledged(second, first.getJSONObject("request").getString("id")))
    }
    @Test fun conversationDraftCannotBecomeAnAgentTask() {
        val draft = SendIntent.freeze(JSONObject(), chat.copy(conversation = true), "hello", "task").getJSONObject("request")
        assertEquals("root-id", draft.getString("conv"))
        assertFalse(draft.has("kind")); assertFalse(draft.has("to")); assertFalse(draft.has("agent_id"))
    }
    private fun persistedThread(saved: JSONObject, selected: Chat = chat): JSONObject {
        val request = saved.getJSONObject("request")
        val message = JSONObject().put("id", request.getString("id")).put("dir", "out")
            .put("body", request.getString("body").trim()).put("kind", request.optString("kind").ifBlank { "message" })
            .put("reply_to", request.optString("reply_to"))
        if (selected.conversation) message.put("id", "physical-envelope").put("lid", request.getString("id"))
        else {
            message.put("to", request.getString("to"))
            if (request.optString("agent_id").isNotBlank()) message.put("target", JSONObject()
                .put("address", request.getString("to")).put("agent_id", request.getString("agent_id")))
        }
        return JSONObject().put("id", selected.id).put("peer", selected.peer)
            .put("messages", org.json.JSONArray().put(message))
    }
    @Test fun crashAfterAdmissionIsRecognizedAcrossRestartDespiteEditsOrDeletion() {
        val saved = SendIntent.freeze(JSONObject(), chat, "  Check this  ", "question")
        val restored = JSONObject(saved.toString())
        val thread = persistedThread(saved)
        val message = thread.getJSONArray("messages").getJSONObject(0)
        message.put("edited", true).put("text", "replacement").put("deleted", true)
            .put("state", "queued").put("state_text", "Sending")
        assertTrue(SendIntent.isPersisted(restored, chat, thread))
        message.put("body", "replacement")
        assertFalse(SendIntent.isPersisted(restored, chat, thread))
    }
    @Test fun everyFrozenRequestFieldAndOwnDirectionMustMatch() {
        val saved = SendIntent.freeze(JSONObject(), chat, "Check this", "task")
        val original = persistedThread(saved)
        val mutations = listOf<(JSONObject) -> Unit>(
            { it.put("id", "another-request") },
            { it.put("dir", "in") },
            { it.put("body", "another body") },
            { it.put("kind", "question") },
            { it.put("reply_to", "another-root") },
            { it.put("to", "other/device") },
            { it.getJSONObject("target").put("address", "other/device") },
            { it.getJSONObject("target").put("agent_id", "other-agent") },
            { it.put("agent_id", "author-agent") },
            { it.put("verified_agent", true) },
            { it.put("quote", "not-requested") },
            { it.put("files", org.json.JSONArray().put(JSONObject().put("name", "extra-file"))) }
        )
        mutations.forEachIndexed { index, mutate ->
            val changed = JSONObject(original.toString())
            mutate(changed.getJSONArray("messages").getJSONObject(0))
            assertFalse("mutation $index", SendIntent.isPersisted(saved, chat, changed))
        }
        assertFalse(SendIntent.isPersisted(saved, chat, JSONObject(original.toString()).put("id", "other-root")))
        assertFalse(SendIntent.isPersisted(saved, chat, JSONObject(original.toString()).put("peer", "other/device")))
        assertFalse(SendIntent.isPersisted(saved, chat.copy(agentId = "other-agent"), original))
    }
    @Test fun duplicateTextWithAnotherIDDoesNotAcknowledgeThePendingSend() {
        val pending = SendIntent.freeze(JSONObject(), chat, "Same text", "question")
        val older = SendIntent.freeze(JSONObject(), chat, "Same text", "question")
        assertFalse(SendIntent.isPersisted(pending, chat, persistedThread(older)))
        assertFalse(SendIntent.isPersisted(JSONObject().put("body", "Same text"), chat, persistedThread(older)))
    }
    @Test fun conversationUsesLogicalIDAndCannotAcknowledgeAnotherConversation() {
        val dm = chat.copy(conversation = true, agentId = "")
        val saved = SendIntent.freeze(JSONObject(), dm, "hello", "message")
        val thread = persistedThread(saved, dm)
        // Human guest author participation is inferred by the existing core,
        // not an agent executor or a new permission in the saved request.
        thread.getJSONArray("messages").getJSONObject(0).put("pid", "human-guest")
        assertTrue(SendIntent.isPersisted(saved, dm, thread))
        assertFalse(SendIntent.isPersisted(saved, dm.copy(id = "different-conversation"), thread))
        val message = thread.getJSONArray("messages").getJSONObject(0)
        message.put("lid", "different-logical-id")
        assertFalse(SendIntent.isPersisted(saved, dm, thread))
        message.put("lid", saved.getJSONObject("request").getString("id")).put("kind", "task")
        assertFalse(SendIntent.isPersisted(saved, dm, thread))
    }
    @Test fun extraFrozenFieldsFailClosedUntilTheyHaveAProjectionMatch() {
        val saved = SendIntent.freeze(JSONObject(), chat, "Check this", "question")
        val thread = persistedThread(saved)
        saved.getJSONObject("request").put("quote", "new-field")
        assertFalse(SendIntent.isPersisted(saved, chat, thread))
    }
}
