package io.github.misunders2d.agentnet

import org.json.JSONObject
import org.junit.Assert.*
import org.junit.Test

class ModelsTest {
    private val chat = Chat("conv", "Person", "", "", 0, true)
    @Test fun deletedAndEditedMessagesUseCanonicalControls() {
        val rows = Models.messages(JSONObject("""{"messages":[
          {"id":"1","dir":"in","body":"private old body","deleted":true},
          {"id":"2","dir":"in","body":"old body","edited":true,"text":"replacement"}
        ]}"""), chat)
        assertEquals("Message deleted", rows[0].body)
        assertEquals("replacement", rows[1].body)
    }
    @Test fun claimedAgentOriginIsNotVerifiedAuthorship() {
        val rows = Models.messages(JSONObject("""{"messages":[{"id":"1","from":"a person","origin":"agent:Codex","body":"hello","verified_agent":false}]}"""), chat)
        assertEquals("a person", rows.single().author)
    }
    @Test fun deliveryTextComesFromCoreAndUnreadIsPreserved() {
        val rows = Models.messages(JSONObject("""{"messages":[{"id":"1","dir":"out","body":"hello","state":"custody","state_text":"At the server","unread":true}]}"""), chat)
        assertEquals("At the server", rows.single().state)
        assertTrue(rows.single().unread)
    }
    @Test fun conversationTopicsAndReviewNoticesDoNotDuplicateChatRows() {
        val rows = Models.chats(JSONObject("""{"dms":[{"id":"dm","peer":{"label":"Sergey"},"last_at":"2026-10-10T10:00:00Z"}],"threads":[{"id":"topic","conv":"dm"},{"id":"notice","notice_only":true},{"id":"direct","peer":"owner/laptop","title":"Agent question","last_at":"2026-10-10T11:00:00Z","agent_id":"agent1"}]}"""))
        assertEquals(listOf("direct", "dm"), rows.map { it.id })
        assertEquals("agent1", rows[0].agentId)
        assertEquals("Sergey", rows[1].title)
    }
    @Test fun fractionalAndOffsetTimestampsSortChronologically() {
        val rows = Models.chats(JSONObject("""{"dms":[
          {"id":"whole","last_at":"2026-10-10T10:00:00Z"},
          {"id":"fraction","last_at":"2026-10-10T10:00:00.500000001Z"},
          {"id":"offset","last_at":"2026-10-10T13:00:01+03:00"},
          {"id":"unknown","last_at":"invalid"}
        ]}"""))
        assertEquals(listOf("offset", "fraction", "whole", "unknown"), rows.map { it.id })
    }
    @Test fun ownVerifiedAgentTurnUsesExactHostAndAuthorParticipation() {
        val rows = Models.messages(JSONObject("""{"agents":[
          {"pid":"executor","host":{"address":"owner/laptop","label":"Sergey"}},
          {"pid":"other","host":{"address":"someone/desk","label":"Someone"}}
        ],"messages":[
          {"id":"1","dir":"out","from":"owner/laptop","pid":"other","agent_author_pid":"executor","verified_agent":true,"body":"result"},
          {"id":"2","dir":"in","from":"attacker/phone","pid":"executor","verified_agent":true,"body":"result"}
        ]}"""), chat)
        assertEquals("Agent on Sergey", rows[0].author)
        assertEquals("Agent on attacker/phone", rows[1].author)
    }
    @Test fun editedAgentTurnDistinguishesThePersonEdit() {
        val row = Models.messages(JSONObject("""{"messages":[
          {"id":"1","dir":"out","from":"owner/laptop","verified_agent":true,"body":"original","edited":true,"text":"human edit","state_text":"Delivered"}
        ]}"""), chat).single()
        assertEquals("human edit", row.body)
        assertTrue(row.author.contains("edited by a person"))
        assertEquals("Delivered · Edited", row.state)
    }
    @Test fun threadProjectionAndCopiedHistoryDoNotInventHumanAuthorship() {
        val rows = Models.messages(JSONObject("""{"messages":[
          {"id":"1","dir":"out","body":"result","author":{"label":"Agent named-id","about":"Named executor asserted by host"}},
          {"id":"2","dir":"out","body":"old text","synced_from":"owner/laptop","author":{"label":"This computer"}},
          {"id":"3","dir":"in","from":"original/device","excerpt_pid":"grant","origin":"agent:claimed","body":"context","verified_agent":true}
        ]}"""), chat)
        assertEquals("Agent named-id", rows[0].author)
        assertTrue(rows[1].author.contains("history from owner/laptop (author not checked here)"))
        assertEquals("Claimed original/device · forwarded context", rows[2].author)
    }
    @Test fun memberAndGuestNamesResolveTheirRosterDevices() {
        val rows = Models.messages(JSONObject("""{"members":[{"label":"Member","address":"member/laptop","devices":[{"address":"member/phone"}]}],
          "guests":[{"host":{"label":"Guest","address":"guest/laptop"}}],
          "messages":[{"id":"1","from":"member/phone","body":"hi"},{"id":"2","from":"guest/laptop","body":"hello"}]}"""), chat)
        assertEquals(listOf("Member", "Guest"), rows.map { it.author })
    }
    @Test fun allSavedRowsRemainReachableAndGroupTitleIsPreserved() {
        val thread = JSONObject().put("messages", org.json.JSONArray((1..240).map {
            JSONObject().put("id", it.toString()).put("body", "message $it")
        }))
        val rows = Models.messages(thread, chat)
        assertEquals(240, rows.size)
        assertEquals("1", rows.first().id)
        assertEquals("240", rows.last().id)
        val group = Models.chats(JSONObject("""{"dms":[{"id":"group","kind":"group","title":"Project team","peer":{"label":"Person"}}]}""")).single()
        assertEquals("Project team", group.title)
    }
    @Test fun directConversationShowsPersonNameNotItsFirstMessage() {
        val row = Models.chats(JSONObject("""{"dms":[{"id":"direct","title":"First message contents","peer":{"label":"Valerii"}}]}""")).single()
        assertEquals("Valerii", row.title)
    }
}
