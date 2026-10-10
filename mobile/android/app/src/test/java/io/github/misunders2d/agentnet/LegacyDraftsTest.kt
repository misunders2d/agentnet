package io.github.misunders2d.agentnet

import org.junit.Assert.*
import org.junit.Test

class LegacyDraftsTest {
    private val key = "dm:" + "a".repeat(64)
    @Test fun acknowledgementNeverRemovesChangedOrUnrelatedValues() {
        assertTrue(LegacyDrafts.acknowledged(key, "saved", "saved"))
        assertFalse(LegacyDrafts.acknowledged(key, "saved", "edited"))
        assertFalse(LegacyDrafts.acknowledged(key, "saved", null))
        assertFalse(LegacyDrafts.acknowledged("token", "saved", "saved"))
    }
    @Test fun recoveryBoundsActualPrivateValues() {
        assertEquals(listOf(key to "draft"), LegacyDrafts.recover(mapOf(key to "draft", "token" to "secret", "thread:bad" to "no")))
        assertTrue(LegacyDrafts.recover(mapOf(key to "x".repeat(128 * 1024 + 1))).isEmpty())
        assertTrue(LegacyDrafts.recover(mapOf(key to "€".repeat(50000))).isEmpty())
        val entries = (0..299).associate { "thread:" + it.toString(16).padStart(32, '0') to "draft" }
        assertEquals(256, LegacyDrafts.recover(entries).size)
        val large = entries.mapValues { "x".repeat(128 * 1024) }
        assertEquals(8, LegacyDrafts.recover(large).size)
    }
}
