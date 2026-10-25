package io.github.misunders2d.agentnet

import org.junit.Assert.*
import org.junit.Test
import org.json.JSONObject

class ActivityBadgeTagTest {
    private val ownPackage = "io.github.misunders2d.agentnet.preview"
    private val route = "msg=" + "a".repeat(32) + "&dir=in"
    private val tag = WebPolicy.notificationTag("default", route)!!
    private fun metadata(instance: String = "b".repeat(32)): String = JSONObject()
        .put("v", 1).put("workspace", "default").put("tag", tag).put("instance", instance).put("category", "activity")
        .put("ref", JSONObject().put("conv", "").put("fingerprint", List(4) { "c".repeat(8) }.joinToString("-"))
            .put("lid", "a".repeat(32))).toString()
    private fun record(raw: String? = metadata(), nativeTag: String? = tag): ActivityNoticeRecord? =
        activityNoticeRecord(ownPackage, ownPackage, ACTIVITY_CHANNEL, 0, nativeTag, raw)

    @Test fun restorationAcceptsOnlyBoundBoundedOwnActivityMetadata() {
        assertNotNull(record())
        assertNull(activityNoticeRecord(ownPackage, "other.package", ACTIVITY_CHANNEL, 0, tag, metadata()))
        assertNull(activityNoticeRecord(ownPackage, ownPackage, "connection", 0, tag, metadata()))
        assertNull(activityNoticeRecord(ownPackage, ownPackage, ACTIVITY_CHANNEL, 1, tag, metadata()))
        for (raw in listOf("", "{}", "x".repeat(NOTICE_METADATA_MAX + 1),
            JSONObject(metadata()).put("v", 2).toString(), JSONObject(metadata()).put("body", "private preview").toString(),
            JSONObject(metadata()).put("workspace", "d".repeat(32)).toString(),
            JSONObject(metadata()).put("tag", "review&workspace=default").toString(),
            JSONObject(metadata()).put("instance", "bad").toString())) assertNull(raw.take(50), record(raw))
        assertNull(record(nativeTag = tag + "&extra=1"))
        assertEquals("", record(null)!!.instance) // Absence alone identifies a legacy note.
    }

    @Test fun startupChunksRetainEveryActiveRecordAndExplicitUnknownState() {
        val records = (0 until 65).map { record(metadata(it.toString(16).padStart(32, '0')))!! }
        val chunks = activityRestoreChunks(records, 3)
        assertEquals(3, chunks.size)
        assertEquals(65, chunks.sumOf { JSONObject(it).getJSONArray("entries").length() })
        assertEquals(3, chunks.sumOf { JSONObject(it).getInt("unknown") })
        assertTrue(chunks.all { JSONObject(it).getBoolean("known") })
        val absent = JSONObject(activityRestoreChunks(emptyList(), 0).single())
        assertEquals(0, absent.getJSONArray("entries").length()) // User-dismissed notes are never reconstructed.
        val failed = JSONObject(activityRestoreChunks(emptyList(), 0, false).single())
        assertFalse(failed.getBoolean("known"))
    }

    @Test fun replacementInstancesFenceQueuedCancelAndStaleBadgeSnapshots() {
        val old = record()!!
        val newer = record(metadata("d".repeat(32)))!!
        val latest = mapOf(tag to newer.instance)
        assertFalse(activityInstanceMatches(newer, old.instance, latest))
        assertFalse(activityInstanceMatches(old, old.instance, latest)) // OS snapshot still shows old after notify.
        assertFalse(activityBadgeEligible(old, emptyMap(), latest))
        assertTrue(activityBadgeEligible(newer, mapOf(tag to old.instance), latest))
        assertFalse(activityBadgeEligible(newer, mapOf(tag to newer.instance), latest))
        assertTrue(activityInstanceMatches(old, old.instance, mapOf(tag to old.instance)))
        // Badge selection and restoration preserve the complete original metadata/instance.
        val restored = JSONObject(activityRestoreChunks(listOf(newer), 0).single()).getJSONArray("entries").getJSONObject(0)
        assertEquals(newer.metadata, restored.getString("metadata"))
        assertEquals(newer.instance, JSONObject(restored.getString("metadata")).getString("instance"))
    }
    @Test fun reminderSummaryHasNoFrozenMembershipOrReference() {
        val summary = JSONObject().put("v", 1).put("workspace", "default").put("tag", "workspace=default")
            .put("instance", "b".repeat(32)).put("category", "reminder-summary")
        assertNotNull(record(summary.toString(), "workspace=default"))
        summary.put("ref", JSONObject(metadata()).getJSONObject("ref"))
        assertNull(record(summary.toString(), "workspace=default"))
        summary.remove("ref")
        summary.put("reminders", org.json.JSONArray().put(JSONObject().put("message", "a".repeat(32)).put("revision", 7)))
        assertNull(record(summary.toString(), "workspace=default"))
    }
    @Test fun onlyKnownExactCountsHaveNumbers() {
        assertEquals(7, activityBadgeNumber(7, false))
        assertEquals(0, activityBadgeNumber(0, false))
        assertEquals(0, activityBadgeNumber(-1, false))
        assertEquals(Int.MAX_VALUE, activityBadgeNumber(Long.MAX_VALUE, false))
    }
    @Test fun PartialPositiveAndZeroClearNumbers() {
        assertEquals(0, activityBadgeNumber(7, true))
        assertEquals(0, activityBadgeNumber(0, true))
        assertEquals(0, activityBadgeNumber(Long.MAX_VALUE, true))
    }
    @Test fun exactExistingNotificationTagsAreEligible() {
        for (workspace in listOf("default", "a".repeat(32))) {
            for (route in listOf("review", "conv=" + "b".repeat(64), "msg=" + "c".repeat(32) + "&dir=in", "")) {
                val tag = WebPolicy.notificationTag(workspace, route)!!
                assertTrue(tag, activityBadgeTag(tag))
            }
        }
        val legacy = WebPolicy.notificationTag("default", "msg=" + "d".repeat(32) + "&conv=" + "e".repeat(64) + "&dir=in")!!
        assertFalse(legacy.contains("conv="))
        assertTrue(activityBadgeTag(legacy))
    }
    @Test fun unrelatedMalformedOrUnboundTagsCannotBeUpdated() {
        for (tag in listOf(null, "connection", "activity:anything", "review", "workspace=unknown", "review&workspace=default&extra=1",
            "msg=bad&dir=in&workspace=default", "review&workspace=default&workspace=default", "review#x&workspace=default", "x".repeat(2049))) {
            assertFalse(tag, activityBadgeTag(tag))
        }
    }
    @Test fun pendingCancellationsExcludeOnlyTheirExactTags() {
        val route = "conv=" + "a".repeat(64)
        val cancelled = WebPolicy.notificationTag("default", route)!!
        val secondCancelled = WebPolicy.notificationTag("default", "msg=" + "b".repeat(32) + "&dir=in")!!
        val healthy = WebPolicy.notificationTag("default", "review")!!
        val otherWorkspace = WebPolicy.notificationTag("c".repeat(32), route)!!
        val staleSnapshot = listOf(cancelled, secondCancelled, healthy, otherWorkspace, "unrelated-tag")
        val pending = mutableSetOf(cancelled)
        assertEquals(listOf(secondCancelled, healthy, otherWorkspace), staleSnapshot.filter { activityBadgeTag(it, pending) })
        pending.add(secondCancelled)
        assertEquals(listOf(healthy, otherWorkspace), staleSnapshot.filter { activityBadgeTag(it, pending) })
        // A genuine new notification clears just its tag, retaining the other cancel.
        pending.remove(cancelled)
        assertEquals(listOf(cancelled, healthy, otherWorkspace), staleSnapshot.filter { activityBadgeTag(it, pending) })
        assertTrue(activityBadgeTag(cancelled))
    }
}
