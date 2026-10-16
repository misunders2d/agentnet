package io.github.misunders2d.agentnet

import org.junit.Assert.*
import org.junit.Test

class ActivityBadgeTagTest {
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
}
