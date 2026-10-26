package io.github.misunders2d.agentnet

import android.app.Activity
import android.app.Instrumentation
import android.content.Intent
import android.os.Bundle
import android.webkit.WebView
import org.json.JSONObject
import java.io.File
import java.security.MessageDigest
import java.util.concurrent.CountDownLatch
import java.util.concurrent.ExecutorService
import java.util.concurrent.TimeUnit

/** Test APK only: the owned emulator fixture, never a production bridge or source override. */
class UpdaterInstrumentation : Instrumentation() {
    private var mode = "install"
    private var latencySuite = "history"
    private var ownedFixture = false
    override fun onCreate(arguments: Bundle?) {
        phase("create-before")
        super.onCreate(arguments)
        mode = arguments?.getString("mode") ?: "install"
        latencySuite = arguments?.getString("latencySuite") ?: "history"
        ownedFixture = arguments?.getString("ownedFixture") == "1"
        phase("instrumentation-thread-before")
        start()
        phase("instrumentation-thread-after")
    }
    // Static labels only: no URLs, identities, input JSON or JavaScript source.
    private fun phase(value: String) = sendStatus(1, Bundle().apply { putString("phase", value) })
    override fun onStart() {
        phase("on-start-before")
        val result = Bundle()
        try {
            check(mode in setOf("install", "snapshot", "network", "cleanup", "interrupt", "notifications", "notifications-denied", "indicators-video", "latency")) { "Unknown fixture operation." }
            check((mode != "latency" && !mode.startsWith("notifications") && mode != "indicators-video") || ownedFixture) {
                "This mode requires the explicitly owned disposable fixture runner."
            }
            if (mode == "cleanup") {
                val preferences=targetContext.applicationContext.getSharedPreferences("native-update",0)
                val id=preferences.getInt("session",-1)
                val installer=targetContext.packageManager.packageInstaller
                val info=installer.getSessionInfo(id)
                check(info?.appPackageName == targetContext.packageName && info.installerPackageName == targetContext.packageName) { "No test-owned fixture session exists." }
                installer.abandonSession(id)
                preferences.edit().remove("session").remove("result").commit()
                result.putString("stream","Discarded test-owned session blocked by harness teardown; this is setup cleanup, not cancellation evidence.\n")
                phase("finish-before")
                finish(Activity.RESULT_OK,result); return
            }
            phase("activity-start-before")
            val activity = startActivitySync(Intent(targetContext, MainActivity::class.java).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)) as MainActivity
            phase("activity-start-after")
            phase("idle-before")
            waitForIdleSync()
            phase("idle-after")
            if (mode == "latency") {
                phase("latency-before")
                checkLatency(activity, result)
                phase("latency-after")
                phase("finish-before")
                finish(Activity.RESULT_OK, result); return
            }
            if (mode == "indicators-video") {
                phase("indicators-before")
                checkIndicatorsVideo(activity, result)
                phase("indicators-after")
                phase("finish-before")
                finish(Activity.RESULT_OK, result); return
            }
            if (mode.startsWith("notifications")) {
                phase("notifications-before")
                checkNotifications(activity, mode == "notifications-denied", result)
                phase("notifications-after")
                phase("finish-before")
                finish(Activity.RESULT_OK, result); return
            }
            if (mode == "snapshot" || mode == "network") {
                if (mode == "network") {
                    snapshot(activity) // Wait for the real embedded host before changing routes.
                    val host = (targetContext.applicationContext as AgentNetApp).host
                    val monitor = NativeHost::class.java.getDeclaredField("networkMonitor").apply { isAccessible = true }.get(host)
                    val route = monitor.javaClass.getDeclaredField("route").apply { isAccessible = true }.get(monitor)
                    val current = route.javaClass.getDeclaredField("current").apply { isAccessible = true }
                    fun handle(): Long? { var value: Long? = null; runOnMainSync { value = current.get(route) as Long? }; return value }
                    val before = handle()
                    sendStatus(0, Bundle().apply { putString("stream", "Waiting for owned-emulator network change; handle=$before\n") })
                    val deadline = System.nanoTime() + TimeUnit.SECONDS.toNanos(30)
                    while (handle() == before && System.nanoTime() < deadline) Thread.sleep(100)
                    val after = handle()
                    check(after != before) { "Android did not report a default-route identity change." }
                    val lane = NativeHost::class.java.getDeclaredField("lane").apply { isAccessible = true }.get(host) as ExecutorService
                    val drained = CountDownLatch(1)
                    lane.execute { drained.countDown() }
                    check(drained.await(5, TimeUnit.SECONDS)) { "The native connection lane did not return." }
                    sendStatus(0, Bundle().apply { putString("stream", "Native network route $before -> $after; connection lane returned.\n") })
                }
                val snapshot = snapshot(activity)
                // Instrumentation runs as the target UID, including release targets.
                // Export only this bounded synthetic fixture, never an entry token/key.
                check(snapshot.toByteArray().size <= 256 * 1024) { "The synthetic snapshot exceeds its result bound." }
                val value = JSONObject(snapshot)
                val digest = MessageDigest.getInstance("SHA-256").digest(value.getJSONObject("preserved").toString().toByteArray()).joinToString("") { "%02x".format(it) }
                result.putString("stream", "SNAPSHOT_JSON=$snapshot\nSnapshot version=${value.getString("version")}; messages=${value.getJSONObject("preserved").getJSONArray("messages").length()}; preserved_sha256=$digest\n")
            } else {
                val fixture = File(targetContext.cacheDir, "updater-fixture.apk")
                check(fixture.isFile) { "Stage the signed fixture APK in the owned target's private cache first." }
                val complete = CountDownLatch(1)
                val terminal = CountDownLatch(1)
                val preferences=targetContext.applicationContext.getSharedPreferences("native-update",0)
                val listener=android.content.SharedPreferences.OnSharedPreferenceChangeListener { p,key ->
                    if(key == "result" && p.getInt("session",-1) < 0) terminal.countDown()
                }
                preferences.registerOnSharedPreferenceChangeListener(listener)
                val diagnostic=Bundle().apply { putString("stream", "Before retry: session=${preferences.getInt("session",-1)}, pending=${preferences.getBoolean("pending_confirmation",false)}, target=${targetContext.dataDir}, activity=${activity.dataDir}\n") }; sendStatus(0,diagnostic)
                var response: JSONObject? = null
                var failure: String? = null
                val callback: (JSONObject?, String?) -> Unit = { value, error -> response = value; failure = error; complete.countDown() }
                val method = MainActivity::class.java.getDeclaredMethod("installPreparedUpdate", NativeUpdater.Prepared::class.java, kotlin.jvm.functions.Function2::class.java)
                method.isAccessible = true
                runOnMainSync { method.invoke(activity, NativeUpdater.Prepared(fixture, "v0.8.19"), callback) }
                check(complete.await(120, TimeUnit.SECONDS)) { "Permission/installer continuation did not complete." }
                check(failure == null) { failure.orEmpty() }
                check(response?.optString("state") == "installer") { "The real installer was not started." }
                if(mode == "interrupt") {
                    // Intentional process interruption only after the platform reported
                    // real pending user action; no preference fabrication.
                    val deadline=System.nanoTime()+TimeUnit.SECONDS.toNanos(10)
                    while(!preferences.getBoolean("pending_confirmation",false) && System.nanoTime()<deadline) Thread.sleep(50)
                    check(preferences.getBoolean("pending_confirmation",false)) { "Android did not report pending confirmation." }
                    val id=preferences.getInt("session",-1)
                    val info=targetContext.packageManager.packageInstaller.getSessionInfo(id)
                    check(info != null && !info.isActive) { "The fixture is not waiting for confirmation." }
                    result.putString("stream","Interrupted real inactive confirmation session=$id; explicit visible retry must replace only this owned session.\n")
                    phase("finish-before")
                    finish(Activity.RESULT_OK,result); return
                }
                // Finishing instrumentation kills the target process. Keep it visible
                // until Android's real callback, or normal package replacement.
                check(terminal.await(120, TimeUnit.SECONDS)) { "Android installation confirmation did not reach a terminal result." }
                preferences.unregisterOnSharedPreferenceChangeListener(listener)
                result.putString("stream", "Real installer callback: ${preferences.getString("result", "")}\n")
            }
            phase("finish-before")
            finish(Activity.RESULT_OK, result)
        } catch (e: Exception) {
            result.putString("stream", result.getString("stream").orEmpty() + "Updater fixture failed: ${e.message}\n")
            phase("finish-failed-before")
            finish(Activity.RESULT_CANCELED, result)
        }
    }

    private fun fixtureNoticeMetadata(workspace: String, fragment: String, category: String = if (fragment == "review") "decision" else "activity"): String {
        val fields = fragment.split('&').associate { it.substringBefore('=') to it.substringAfter('=', "") }
        val message = fields["msg"] ?: "4".repeat(32)
        return JSONObject().put("v", 1).put("workspace", workspace)
            .put("tag", checkNotNull(WebPolicy.notificationTag(workspace, fragment)))
            .put("instance", java.util.UUID.randomUUID().toString().replace("-", ""))
            .put("category", category).apply {
                if (category == "activity" || category == "reminder") put("ref", JSONObject()
                    .put("conv", fields["conv"].orEmpty()).put("lid", message)
                    .put("fingerprint", List(4) { "5".repeat(8) }.joinToString("-")))
                if (category == "reminder") put("reminders", org.json.JSONArray().put(JSONObject().put("message", message).put("revision", 7)))
            }.toString()
    }

    /** OS-boundary test on the owned emulator; never grants or changes permissions. */
    private fun checkNotifications(activity: MainActivity, denied: Boolean, result: Bundle) {
        val app = targetContext.applicationContext as AgentNetApp
        val host = app.host
        // Disposable preferences exercise the actual consent implementation,
        // without changing an Android permission or starting a live service.
        val consentPreferences = targetContext.getSharedPreferences("notification-consent-fixture", 0)
        consentPreferences.edit().clear().commit()
        var starts = 0
        startConnectionAfterConsent(consentPreferences, false) { starts++ }
        check(!consentPreferences.getBoolean("keep-connected", false) && starts == 0 && !consentPreferences.contains("keep-connected"))
        startConnectionAfterConsent(consentPreferences, true) { starts++ }
        check(consentPreferences.getBoolean("keep-connected", false) && starts == 1)
        startConnectionAfterConsent(targetContext.getSharedPreferences("notification-consent-fixture", 0), true) { starts++ }
        check(starts == 1) // saved on survives restart without a second consent start
        consentPreferences.edit().putBoolean("keep-connected", false).commit()
        startConnectionAfterConsent(consentPreferences, true) { starts++ }
        check(!consentPreferences.getBoolean("keep-connected", false) && starts == 1) // explicit off wins
        consentPreferences.edit().clear().commit()
        var refused = false
        try { startConnectionAfterConsent(consentPreferences, true) { throw IllegalStateException("Fixture service refusal") } }
        catch (_: IllegalStateException) { refused = true }
        check(refused && !consentPreferences.getBoolean("keep-connected", false))
        consentPreferences.edit().clear().commit()
        // In-memory OS channel objects exercise the actual status/intent code;
        // no saved app channel, permission, sound mode or DND choice is changed.
        val fresh = activityNotificationChannel()
        val freshStatus = activityNotificationStatus(true, fresh)
        check(freshStatus.getBoolean("granted") && freshStatus.getBoolean("app_granted"))
        check(freshStatus.getJSONObject("channel").getBoolean("sound") && freshStatus.getJSONObject("channel").getBoolean("vibration"))
        fresh.enableVibration(false)
        check(!activityNotificationStatus(true, fresh).getJSONObject("channel").getBoolean("vibration"))
        check(!fresh.shouldVibrate()) // reading a legacy vibration-off channel never changes it
        val muted = android.app.NotificationChannel("fixture-muted", "Fixture", android.app.NotificationManager.IMPORTANCE_NONE)
        check(!activityNotificationStatus(true, muted).getBoolean("granted"))
        check(activityNotificationStatus(true, muted).getBoolean("app_granted"))
        check(!activityNotificationStatus(false, fresh).getBoolean("granted"))
        check(!activityNotificationStatus(true, null).has("channel"))
        val settings = activityNotificationSettings(targetContext.packageName)
        check(settings.action == android.provider.Settings.ACTION_CHANNEL_NOTIFICATION_SETTINGS)
        check(settings.getStringExtra(android.provider.Settings.EXTRA_APP_PACKAGE) == targetContext.packageName)
        val connectionSettings = connectionNotificationSettings(targetContext.packageName)
        check(connectionSettings.action == android.provider.Settings.ACTION_CHANNEL_NOTIFICATION_SETTINGS)
        check(connectionSettings.getStringExtra(android.provider.Settings.EXTRA_APP_PACKAGE) == targetContext.packageName)
        check(connectionSettings.getStringExtra(android.provider.Settings.EXTRA_CHANNEL_ID) == CONNECTION_CHANNEL)
        check(settings.getStringExtra(android.provider.Settings.EXTRA_CHANNEL_ID) == ACTIVITY_CHANNEL)
        val manager = targetContext.getSystemService(android.app.NotificationManager::class.java)
        val post = NativeHost::class.java.getDeclaredMethod("notifyActivity", String::class.java, String::class.java, String::class.java, String::class.java, String::class.java).apply { isAccessible = true }
        val dismiss = NativeHost::class.java.getDeclaredMethod("dismissActivity", String::class.java, String::class.java, String::class.java).apply { isAccessible = true }
        val startup = NativeHost::class.java.getDeclaredMethod("snapshotActivityNotices").apply { isAccessible = true }
        val workspace = "default"
        val routes = listOf("conv=" + "a".repeat(64), "review", "msg=" + "b".repeat(32) + "&dir=in")
        val tags = routes.map { WebPolicy.notificationTag(workspace, it)!! }.toSet()
        val emitted = mutableMapOf<String, String>()
        fun active() = manager.activeNotifications.filter { it.packageName == targetContext.packageName && it.id == 0 && it.notification.channelId == ACTIVITY_CHANNEL && it.tag in tags }
        fun instance(fragment: String) = JSONObject(active().single { it.tag == WebPolicy.notificationTag(workspace, fragment) }
            .notification.extras.getString(NOTICE_METADATA_EXTRA)!!).getString("instance")
        fun emit(fragment: String, title: String = "Fixture sender", body: String = "Locally decrypted fixture preview") {
            val metadata = fixtureNoticeMetadata(workspace, fragment, if (fragment == routes[2]) "reminder" else if (fragment == "review") "decision" else "activity")
            emitted[WebPolicy.notificationTag(workspace, fragment)!!] = metadata
            runOnMainSync { post.invoke(host, workspace, fragment, title, body, metadata) }
        }
        fun currentEmissions() = active().all { it.notification.extras.getString(NOTICE_METADATA_EXTRA) == emitted[it.tag] }
        fun dismissCurrent(fragment: String) = runOnMainSync { dismiss.invoke(host, workspace, fragment, instance(fragment)) }
        fun waitFor(label: String, predicate: () -> Boolean) {
            val end = System.nanoTime() + TimeUnit.SECONDS.toNanos(5)
            while (!predicate() && System.nanoTime() < end) Thread.sleep(50)
            check(predicate()) {
                val observed = active().map { entry -> "${tags.indexOf(entry.tag)}:current=${entry.notification.extras.getString(NOTICE_METADATA_EXTRA) == emitted[entry.tag]}" }
                "$label; owned active routes=$observed; expected count=${emitted.size}"
            }
        }
        val totalField = NativeHost::class.java.getDeclaredField("unreadTotal").apply { isAccessible = true }
        val partialField = NativeHost::class.java.getDeclaredField("unreadPartial").apply { isAccessible = true }
        val dismissedField = NativeHost::class.java.getDeclaredField("dismissedActivityTags").apply { isAccessible = true }
        val latestField = NativeHost::class.java.getDeclaredField("latestActivityInstances").apply { isAccessible = true }
        @Suppress("UNCHECKED_CAST")
        fun pendingTags() = dismissedField.get(host) as MutableMap<String, String> // main-thread calls only
        @Suppress("UNCHECKED_CAST")
        fun latestInstances() = latestField.get(host) as MutableMap<String, String> // main-thread calls only
        var savedTotal = 0L; var savedPartial = true; var restoreBadges = false
        val barrierTag = "fixture-badge-barrier"
        var barrier = 0
        try {
            // These invented OS notices must not enter the real core's startup
            // restore snapshot: their IDs deliberately have no stored message.
            // Wait for that existing startup boundary before posting the fixture.
            waitFor("Owned native host did not finish startup before notification fixture") {
                var ready = false
                runOnMainSync { ready = host.state.url.isNotEmpty() }
                ready
            }
            check(host.notificationsPermitted() != denied) { "Expected Android permission ${if (denied) "denied" else "granted"}; this test never changes it." }
            runOnMainSync { tags.forEach { tag -> manager.cancel(tag, 0); pendingTags().remove(tag); latestInstances().remove(tag) } }
            if (denied) {
                routes.forEach { emit(it) }
                waitFor("Denied notifications appeared") { active().isEmpty() }
                result.putString("stream", "PASS Android denied permission: no notification posted; permissions unchanged.\n")
                return
            }
            runOnMainSync {
                savedTotal = totalField.getLong(host); savedPartial = partialField.getBoolean(host)
                restoreBadges = true
                totalField.setLong(host, 7L); partialField.setBoolean(host, false)
            }
            // Foreground, then background, then screen-off arrival use the same
            // actual NotificationManager surface. Core event producers are tested in Go.
            emit(routes[0]); emit(routes[0])
            waitFor("Foreground notification/dedup failed") { active().size == 1 && currentEmissions() }
            runOnMainSync { activity.moveTaskToBack(true) }
            emit(routes[1])
            waitFor("Background OK notification missing") { active().size == 2 && currentEmissions() }
            uiAutomation.executeShellCommand("input keyevent 223").close()
            emit(routes[2])
            waitFor("Screen-off message/reminder route missing") { active().size == 3 && currentEmissions() }
            check(active().all { entry ->
                val metadata = checkNotNull(entry.notification.extras.getString(NOTICE_METADATA_EXTRA))
                !metadata.contains("Fixture sender") && !metadata.contains("preview") &&
                    activityNoticeRecord(targetContext.packageName, entry.packageName, entry.notification.channelId, entry.id, entry.tag, metadata) != null
            }) { "Android did not retain the bound content-free notice metadata" }
            check(active().all { it.notification.extras.getCharSequence(android.app.Notification.EXTRA_TITLE).toString() == "Fixture sender" })
            check(active().all { it.notification.extras.getCharSequence(android.app.Notification.EXTRA_TEXT).toString() == "Locally decrypted fixture preview" })
            check(active().all { it.notification.publicVersion.extras.getCharSequence(android.app.Notification.EXTRA_TITLE).toString() == "AgentNet" })
            check(active().all { !it.notification.publicVersion.extras.getCharSequence(android.app.Notification.EXTRA_TEXT).toString().contains("fixture preview") })
            check(active().all { it.notification.visibility == android.app.Notification.VISIBILITY_PRIVATE })
            uiAutomation.executeShellCommand("input keyevent 224").close()
            val target = active().single { it.tag == WebPolicy.notificationDestination(workspace, routes[2]) }
            target.notification.contentIntent.send()
            waitFor("Notification tap lost exact destination") {
                var matches = false
                runOnMainSync { matches = activity.intent.getStringExtra("workspace") == workspace && activity.intent.getStringExtra("destination") == routes[2] }
                matches
            }
            dismissCurrent(routes[0])
            waitFor("Reading exact chat did not dismiss its notification") { active().none { it.tag == WebPolicy.notificationDestination(workspace, routes[0]) } }
            check(active().any { it.tag == WebPolicy.notificationDestination(workspace, routes[1]) }) { "Reading another chat cleared an OK" }
            // Reuse this mode's real NativeHost/NotificationManager with positive
            // counts. The private map models a cancel still visible in an OS
            // snapshot; it is not a substitute for the actual cancel below.
            emit(routes[1]); emit(routes[2]); emit(routes[0])
            waitFor("Positive badge fixture notices missing") { active().size == 3 && currentEmissions() }
            val retainedMetadata = active().associate { it.tag to it.notification.extras.getString(NOTICE_METADATA_EXTRA) }
            fun refreshUnread(total: Long) = runOnMainSync {
                totalField.setLong(host, total); partialField.setBoolean(host, false)
                updateActivityBadges(app, total, false, pendingTags(), latestInstances()) // same projection as UnreadSink
            }
            fun settleBadgePosts() {
                val number = ++barrier
                val template = active().first().notification
                runOnMainSync { manager.notify("$barrierTag-$number", 83, android.app.Notification.Builder.recoverBuilder(app, template)
                    .setNumber(number).setOnlyAlertOnce(true).build()) }
                // An unrelated, ineligible tag queued after the badge writes
                // fences their OS processing, without sleeping or longer waits.
                waitFor("Queued badge writes did not reach Android") {
                    manager.activeNotifications.any { it.tag == "$barrierTag-$number" && it.notification.number == number }
                }
            }
            fun exactCount(total: Int) = active().count { it.notification.number == total } == 1 &&
                active().all { it.notification.number == 0 || it.notification.number == total }
            refreshUnread(7); settleBadgePosts()
            waitFor("Positive aggregate badge missing") { exactCount(7) }
            check(active().all { it.notification.extras.getString(NOTICE_METADATA_EXTRA) == retainedMetadata[it.tag] }) {
                "Badge recovery changed native notice metadata/instance"
            }
            val settled = active().associate { it.key to (it.postTime to it.notification.number) }
            refreshUnread(7); settleBadgePosts()
            check(active().associate { it.key to (it.postTime to it.notification.number) } == settled) {
                "Same unread total unnecessarily reposted notifications"
            }
            val cancelledTag = WebPolicy.notificationTag(workspace, routes[0])!!
            val beforeExcluded = active().single { it.tag == cancelledTag }
            runOnMainSync { pendingTags()[cancelledTag] = instance(routes[0]) }
            refreshUnread(5); settleBadgePosts()
            waitFor("Healthy notice lost its count during pending cancellation") {
                active().count { it.tag != cancelledTag && it.notification.number == 5 } == 1
            }
            val excluded = active().single { it.tag == cancelledTag }
            check(excluded.postTime == beforeExcluded.postTime && excluded.notification.number == beforeExcluded.notification.number) {
                "Still-active cancelled tag was reposted by unread update"
            }
            runOnMainSync {
                dismiss.invoke(host, workspace, routes[0], instance(routes[0]))
                totalField.setLong(host, 4L); updateActivityBadges(app, 4, false, pendingTags(), latestInstances())
                dismiss.invoke(host, workspace, routes[1], instance(routes[1]))
                totalField.setLong(host, 3L); updateActivityBadges(app, 3, false, pendingTags(), latestInstances())
            }
            settleBadgePosts()
            val survivorTag = WebPolicy.notificationTag(workspace, routes[2])!!
            waitFor("Unread or second cancel resurrected a read notification") {
                active().singleOrNull()?.let { it.tag == survivorTag && it.notification.number == 3 } == true
            }
            emit(routes[0], "Fresh fixture sender", "New unseen fixture preview")
            waitFor("New activity for a read chat was suppressed") {
                active().any { it.tag == cancelledTag && it.notification.extras.getCharSequence(android.app.Notification.EXTRA_TEXT).toString() == "New unseen fixture preview" } && currentEmissions()
            }
            runOnMainSync { check(cancelledTag !in pendingTags()) { "New arrival retained pending cancellation" } }
            val oldInstance = instance(routes[0])
            emit(routes[0], "Replacement fixture sender", "Replacement fixture preview")
            waitFor("Same-tag replacement instance missing") { currentEmissions() && instance(routes[0]) != oldInstance }
            val replacement = active().single { it.tag == cancelledTag }
            val replacementMetadata = replacement.notification.extras.getString(NOTICE_METADATA_EXTRA)
            val replacementInstance = instance(routes[0])
            runOnMainSync { dismiss.invoke(host, workspace, routes[0], oldInstance) }
            settleBadgePosts()
            val afterStale = active().single { it.tag == cancelledTag }
            check(afterStale.postTime == replacement.postTime && afterStale.notification.extras.getString(NOTICE_METADATA_EXTRA) == replacementMetadata) {
                "Queued stale cancellation erased or reposted a newer instance"
            }

            fun nativeSnapshot(): Map<String, String> {
                var chunks = emptyList<String>()
                runOnMainSync {
                    @Suppress("UNCHECKED_CAST")
                    val captured = startup.invoke(host) as List<String>
                    chunks = captured
                }
                check(chunks.isNotEmpty()) { "Native startup snapshot silently omitted its known/unknown state" }
                val records = linkedMapOf<String, String>()
                for (chunk in chunks) {
                    val value = JSONObject(chunk)
                    check(value.getInt("v") == 1 && value.getBoolean("known")) { "Owned OS snapshot was not known" }
                    val entries = value.getJSONArray("entries")
                    check(entries.length() <= NOTICE_RESTORE_CHUNK)
                    for (index in 0 until entries.length()) {
                        val entry = entries.getJSONObject(index)
                        val tag = entry.getString("tag")
                        check(records.put(tag, entry.getString("metadata")) == null) { "OS snapshot duplicated a canonical activity tag" }
                    }
                }
                return records
            }
            val beforeSnapshot = active().associate { it.tag to (it.postTime to it.notification.extras.getString(NOTICE_METADATA_EXTRA)) }
            // Model only fresh native memory. The actual OS objects survive;
            // Go close/reopen and state decisions have separate real-core tests.
            runOnMainSync { tags.forEach { latestInstances().remove(it); pendingTags().remove(it) } }
            val recovered = nativeSnapshot()
            check(beforeSnapshot.all { (tag, value) -> recovered[tag] == value.second }) { "Startup snapshot lost active metadata" }
            runOnMainSync {
                beforeSnapshot.forEach { (tag, value) -> check(latestInstances()[tag] == JSONObject(checkNotNull(value.second)).getString("instance")) }
            }
            check(active().associate { it.tag to (it.postTime to it.notification.extras.getString(NOTICE_METADATA_EXTRA)) } == beforeSnapshot) {
                "Snapshot reconstruction reannounced an existing OS notification"
            }
            // OS cancellation is the same absence surface as a user swipe.
            // Snapshot/number refresh must never reconstruct the absent object.
            runOnMainSync { manager.cancel(survivorTag, 0) }
            waitFor("Fixture user-dismissal absence missing") { active().none { it.tag == survivorTag } }
            check(survivorTag !in nativeSnapshot()) { "Snapshot restored a user-dismissed notification" }
            refreshUnread(3); settleBadgePosts()
            check(active().none { it.tag == survivorTag }) { "Badge refresh recreated a user-dismissed notification" }
            runOnMainSync { dismiss.invoke(host, workspace, routes[0], replacementInstance) }
            waitFor("Current exact instance did not cancel") { active().isEmpty() }
            check(nativeSnapshot().keys.none { it in tags }) { "Empty fixture snapshot re-created a notification" }
            refreshUnread(12)
            check(active().isEmpty()) { "Empty snapshot or badge projection reannounced fixture activity" }
            result.putString("stream", "PASS Android OS notification boundary: foreground/dedup, background OK, screen-off message/reminder route, private content, exact tap/read dismissal, positive pending-cancel/unread/second-cancel/new-arrival, settled same-total badges, retained metadata, stale/current exact-instance controls, fresh-memory startup snapshot and dismissed/empty no-reannouncement checks. Snapshot seam only; real-core reopen is tested separately. No physical-phone or relay delivery claim.\n")
        } finally {
            uiAutomation.executeShellCommand("input keyevent 224").close()
            runOnMainSync {
                // Exact fixture cleanup is not conditional-cancel evidence.
                tags.forEach { tag -> manager.cancel(tag, 0); pendingTags().remove(tag); latestInstances().remove(tag) }
                (1..barrier).forEach { manager.cancel("$barrierTag-$it", 83) }
                if (restoreBadges) {
                    totalField.setLong(host, savedTotal); partialField.setBoolean(host, savedPartial)
                    updateActivityBadges(app, savedTotal, savedPartial, pendingTags(), latestInstances())
                }
            }
        }
    }
    /** Authored OS-boundary matrix; run only on the owned isolated Android fixture. */
    private fun checkIndicatorsVideo(activity: MainActivity, result: Bundle) {
        val app = targetContext.applicationContext as AgentNetApp
        val manager = app.getSystemService(android.app.NotificationManager::class.java)
        check(app.host.notificationsPermitted()) { "Grant existing fixture notification consent before this mode; this test never changes permissions." }
        check(!connectionNotificationChannel().canShowBadge())
        // The registered object, rather than the fresh defaults, owns the
        // setting after an upgrade. Re-creation must preserve that choice.
        val savedConnection = manager.getNotificationChannel(CONNECTION_CHANNEL)
        if (savedConnection != null) {
            val badging = savedConnection.canShowBadge()
            manager.createNotificationChannel(connectionNotificationChannel())
            check(manager.getNotificationChannel(CONNECTION_CHANNEL).canShowBadge() == badging)
        }
        app.host.prepareNotificationChannel()
        val indicatorTotalField = NativeHost::class.java.getDeclaredField("unreadTotal").apply { isAccessible = true }
        val indicatorPartialField = NativeHost::class.java.getDeclaredField("unreadPartial").apply { isAccessible = true }
        val indicatorDismissedField = NativeHost::class.java.getDeclaredField("dismissedActivityTags").apply { isAccessible = true }
        val indicatorLatestField = NativeHost::class.java.getDeclaredField("latestActivityInstances").apply { isAccessible = true }
        @Suppress("UNCHECKED_CAST")
        fun pendingTags() = indicatorDismissedField.get(app.host) as MutableMap<String, String> // main-thread calls only
        @Suppress("UNCHECKED_CAST")
        fun latestInstances() = indicatorLatestField.get(app.host) as MutableMap<String, String> // main-thread calls only
        var indicatorExpectedTotal = 0L; var indicatorExpectedPartial = true
        var indicatorSavedTotal = 0L; var indicatorSavedPartial = true
        fun eligible() = manager.activeNotifications.filter { it.packageName == targetContext.packageName && it.id == 0 && it.notification.channelId == ACTIVITY_CHANNEL && activityBadgeTag(it.tag) }
        var lastIndicatorProjection = 0L
        fun projectIndicatorBadge(total: Long, partial: Boolean) {
            // This direct-helper matrix bypasses the core's coalescing producer.
            // Keep distinct test inputs below Android's notification update rate.
            val spacing = 1000L - (android.os.SystemClock.elapsedRealtime() - lastIndicatorProjection)
            if (spacing > 0) Thread.sleep(spacing)
            lastIndicatorProjection = android.os.SystemClock.elapsedRealtime()
            indicatorExpectedTotal = total; indicatorExpectedPartial = partial
            runOnMainSync {
                // Match the cached scalar projection used by the real UnreadSink.
                indicatorTotalField.setLong(app.host, total)
                indicatorPartialField.setBoolean(app.host, partial)
                updateActivityBadges(app, total, partial, pendingTags(), latestInstances())
            }
        }
        fun waitFor(label: String, predicate: () -> Boolean) {
            val end = System.nanoTime() + TimeUnit.SECONDS.toNanos(5)
            while (!predicate() && System.nanoTime() < end) Thread.sleep(25)
            check(predicate()) {
                // Scalars and fixture numeric IDs only; never notification content or routes.
                val diagnostic = JSONObject().put("expectedTotal", indicatorExpectedTotal)
                    .put("expectedPartial", indicatorExpectedPartial)
                runOnMainSync {
                    diagnostic.put("hostTotal", indicatorTotalField.getLong(app.host))
                        .put("hostPartial", indicatorPartialField.getBoolean(app.host))
                }
                val counts = org.json.JSONArray()
                eligible().forEach { counts.put(JSONObject().put("id", it.id).put("number", it.notification.number)) }
                diagnostic.put("eligibleCounts", counts)
                "$label INDICATOR_FAILURE_JSON=$diagnostic"
            }
        }
        val field = MainActivity::class.java.getDeclaredField("web").apply { isAccessible = true }
        var page: WebView? = null
        waitFor("Native WebView not ready") { runOnMainSync { page = field.get(activity) as? WebView }; page != null }
        val web = checkNotNull(page)
        waitFor("Native WebView not loaded") { var ready = false; runOnMainSync { ready = web.url?.startsWith(app.host.state.origin) == true }; ready }
        runOnMainSync {
            indicatorSavedTotal = indicatorTotalField.getLong(app.host)
            indicatorSavedPartial = indicatorPartialField.getBoolean(app.host)
        }
        check(eligible().isEmpty()) { "Use the isolated fixture without pre-existing activity notifications." }
        val tags = listOf("1".repeat(32), "2".repeat(32)).map { WebPolicy.notificationTag("default", "msg=$it&dir=in")!! }
        val click = android.app.PendingIntent.getActivity(app, 71, Intent(app, MainActivity::class.java).setAction("fixture-indicator"), android.app.PendingIntent.FLAG_IMMUTABLE)
        val redacted = android.app.Notification.Builder(app, ACTIVITY_CHANNEL).setSmallIcon(R.drawable.ic_agentnet)
            .setContentTitle("AgentNet").setContentText("New activity").build()
        fun note(metadata: String? = null) = android.app.Notification.Builder(app, ACTIVITY_CHANNEL).setSmallIcon(R.drawable.ic_agentnet)
            .setContentTitle("Fixture sender").setContentText("Private retained preview")
            .setStyle(android.app.Notification.BigTextStyle().bigText("Private retained preview"))
            .setContentIntent(click).setAutoCancel(true).setVisibility(android.app.Notification.VISIBILITY_PRIVATE)
            .setPublicVersion(redacted).apply { if (metadata != null) addExtras(Bundle().apply { putString(NOTICE_METADATA_EXTRA, metadata) }) }.build()
        try {
            projectIndicatorBadge(9, false)
            check(eligible().isEmpty()) { "Badge-only notification appeared" }
            runOnMainSync {
                tags.forEach { tag -> manager.notify(tag, 0, note(fixtureNoticeMetadata("default", tag))) }
                manager.notify("unrelated-tag", 73, note())
            }
            waitFor("Fixture notifications not active") { eligible().size == 2 && manager.activeNotifications.any { it.tag == "unrelated-tag" } }
            val before = eligible().associateBy { it.tag }
            val newestKey = before.values.sortedWith(compareByDescending<android.service.notification.StatusBarNotification> { it.notification.`when` }.thenBy { it.key }).first().key
            projectIndicatorBadge(7, false)
            waitFor("Aggregate not assigned to exactly one active notification") { eligible().count { it.notification.number == 7 } == 1 && eligible().count { it.notification.number == 0 } == 1 }
            check(eligible().single { it.notification.number == 7 }.key == newestKey)
            for (entry in eligible()) {
                val prior = before.getValue(entry.tag).notification
                val now = entry.notification
                check(now.channelId == prior.channelId && now.contentIntent == prior.contentIntent && now.`when` == prior.`when`)
                check(now.visibility == prior.visibility && now.flags and android.app.Notification.FLAG_AUTO_CANCEL != 0)
                check(now.extras.getCharSequence(android.app.Notification.EXTRA_TEXT).toString() == "Private retained preview")
                check(now.extras.getCharSequence(android.app.Notification.EXTRA_BIG_TEXT).toString() == "Private retained preview")
                check(now.publicVersion.extras.getCharSequence(android.app.Notification.EXTRA_TEXT).toString() == "New activity")
                check(now.extras.getString(NOTICE_METADATA_EXTRA) == prior.extras.getString(NOTICE_METADATA_EXTRA)) {
                    "Quiet badge recovery changed retained metadata/instance"
                }
                check(JSONObject(checkNotNull(now.extras.getString(NOTICE_METADATA_EXTRA))).getString("instance") ==
                    JSONObject(checkNotNull(prior.extras.getString(NOTICE_METADATA_EXTRA))).getString("instance"))
                if (now.number == 7) check(now.flags and android.app.Notification.FLAG_ONLY_ALERT_ONCE != 0)
            }
            check(manager.activeNotifications.single { it.tag == "unrelated-tag" }.notification.number == 0)
            projectIndicatorBadge(5, true)
            waitFor("Partial positive estimate did not clear numeric badge") { eligible().all { it.notification.number == 0 } }
            projectIndicatorBadge(0, true)
            check(eligible().all { it.notification.number == 0 })
            projectIndicatorBadge(5, false)
            waitFor("Exact aggregate not restored") { eligible().count { it.notification.number == 5 } == 1 }
            val numbered = eligible().single { it.notification.number == 5 }
            runOnMainSync { manager.cancel(numbered.tag, numbered.id) }
            waitFor("Exact dismissal missing") { eligible().size == 1 }
            projectIndicatorBadge(2, false)
            waitFor("Remaining notification count missing") { eligible().single().notification.number == 2 }
            projectIndicatorBadge(0, true)
            waitFor("Unknown numeric badge not cleared") { eligible().all { it.notification.number == 0 } }
        } finally {
            runOnMainSync {
                tags.forEach { tag -> manager.cancel(tag, 0); pendingTags().remove(tag); latestInstances().remove(tag) }
                manager.cancel("unrelated-tag", 73)
                indicatorTotalField.setLong(app.host, indicatorSavedTotal)
                indicatorPartialField.setBoolean(app.host, indicatorSavedPartial)
            }
        }
        waitFor("Fixture badge cleanup") { eligible().isEmpty() }
        runOnMainSync { updateActivityBadges(app, 12, false, pendingTags(), latestInstances()) }
        check(eligible().isEmpty())
        // A newly posted ordinary activity notification uses the latest scalar.
        val totalField = NativeHost::class.java.getDeclaredField("unreadTotal").apply { isAccessible = true }
        val partialField = NativeHost::class.java.getDeclaredField("unreadPartial").apply { isAccessible = true }
        val post = NativeHost::class.java.getDeclaredMethod("notifyActivity", String::class.java, String::class.java, String::class.java, String::class.java, String::class.java).apply { isAccessible = true }
        val dismiss = NativeHost::class.java.getDeclaredMethod("dismissActivity", String::class.java, String::class.java, String::class.java).apply { isAccessible = true }
        val freshRoute = "msg=" + "3".repeat(32) + "&dir=in"
        var savedTotal = 0L; var savedPartial = true
        try {
            runOnMainSync {
                savedTotal = totalField.getLong(app.host); savedPartial = partialField.getBoolean(app.host)
                totalField.setLong(app.host, 6L); partialField.setBoolean(app.host, false)
                post.invoke(app.host, "default", freshRoute, "Fresh fixture sender", "Fresh private body", fixtureNoticeMetadata("default", freshRoute))
            }
            waitFor("New activity did not use latest aggregate") { eligible().singleOrNull()?.notification?.number == 6 }
            runOnMainSync {
                partialField.setBoolean(app.host, true)
                post.invoke(app.host, "default", freshRoute, "Fresh fixture sender", "Fresh private body", fixtureNoticeMetadata("default", freshRoute))
            }
            waitFor("New activity exposed partial positive estimate as an exact number") { eligible().singleOrNull()?.notification?.number == 0 }
        } finally {
            runOnMainSync {
                val metadata = eligible().singleOrNull()?.notification?.extras?.getString(NOTICE_METADATA_EXTRA)
                val expected = metadata?.let { JSONObject(it).getString("instance") }.orEmpty()
                dismiss.invoke(app.host, "default", freshRoute, expected)
                totalField.setLong(app.host, savedTotal); partialField.setBoolean(app.host, savedPartial)
            }
        }
        waitFor("Fresh activity cleanup") { eligible().isEmpty() }

        javascript(web, "window.__fullscreenBackCalls=0;window.agentnetNativeBack=()=>{window.__fullscreenBackCalls++;return false}")
        var chrome: android.webkit.WebChromeClient? = null
        runOnMainSync { chrome = web.webChromeClient }
        val client = checkNotNull(chrome)
        var firstHidden = 0; var secondHidden = 0; var rejectedReentry = 0
        val first = android.view.View(activity)
        var oldUi = 0; var oldFlags = 0; var oldBehavior = 0; var oldStatus = true; var oldNavigation = true
        runOnMainSync {
            val decor = activity.window.decorView
            @Suppress("DEPRECATION")
            oldUi = decor.systemUiVisibility
            oldFlags = activity.window.attributes.flags
            if (android.os.Build.VERSION.SDK_INT >= 30) {
                oldBehavior = activity.window.insetsController?.systemBarsBehavior ?: 0
                oldStatus = decor.rootWindowInsets?.isVisible(android.view.WindowInsets.Type.statusBars()) ?: true
                oldNavigation = decor.rootWindowInsets?.isVisible(android.view.WindowInsets.Type.navigationBars()) ?: true
            }
            client.onShowCustomView(first, android.webkit.WebChromeClient.CustomViewCallback {
                firstHidden++
                client.onHideCustomView() // duplicate/reentrant hide
                client.onShowCustomView(android.view.View(activity), android.webkit.WebChromeClient.CustomViewCallback { rejectedReentry++ })
            })
            check(first.parent === decor && first.layoutParams.width == -1 && first.layoutParams.height == -1)
            client.onShowCustomView(android.view.View(activity), android.webkit.WebChromeClient.CustomViewCallback { secondHidden++ })
            check(secondHidden == 1 && first.parent === decor)
        }
        if (android.os.Build.VERSION.SDK_INT >= 30) waitFor("Fullscreen bars did not hide") {
            var hidden = false
            runOnMainSync { val insets = activity.window.decorView.rootWindowInsets; hidden = insets != null && !insets.isVisible(android.view.WindowInsets.Type.statusBars()) && !insets.isVisible(android.view.WindowInsets.Type.navigationBars()) }
            hidden
        } else runOnMainSync {
            @Suppress("DEPRECATION")
            check(activity.window.decorView.systemUiVisibility and android.view.View.SYSTEM_UI_FLAG_FULLSCREEN != 0)
        }
        runOnMainSync {
            val decor = activity.window.decorView
            @Suppress("DEPRECATION")
            activity.onBackPressed()
            check(first.parent == null && firstHidden == 1 && rejectedReentry == 1)
            client.onHideCustomView(); check(firstHidden == 1)
            @Suppress("DEPRECATION")
            check(decor.systemUiVisibility == oldUi)
            check(activity.window.attributes.flags == oldFlags)
            if (android.os.Build.VERSION.SDK_INT >= 30) check(activity.window.insetsController?.systemBarsBehavior == oldBehavior)
        }
        check(javascript(web, "window.__fullscreenBackCalls") == "0" && !activity.isFinishing) { "Fullscreen Back reached page navigation" }
        if (android.os.Build.VERSION.SDK_INT >= 30) waitFor("Original system bars not restored") {
            var restored = false
            runOnMainSync { val insets = activity.window.decorView.rootWindowInsets; restored = insets != null && insets.isVisible(android.view.WindowInsets.Type.statusBars()) == oldStatus && insets.isVisible(android.view.WindowInsets.Type.navigationBars()) == oldNavigation }
            restored
        }
        var pausedHidden = 0; val paused = android.view.View(activity)
        runOnMainSync {
            client.onShowCustomView(paused, android.webkit.WebChromeClient.CustomViewCallback { pausedHidden++ })
            callActivityOnPause(activity)
            check(paused.parent == null && pausedHidden == 1)
            client.onHideCustomView(); check(pausedHidden == 1)
            callActivityOnResume(activity)
        }
        var destroyedHidden = 0; val last = android.view.View(activity)
        runOnMainSync {
            client.onShowCustomView(last, android.webkit.WebChromeClient.CustomViewCallback { destroyedHidden++ })
            check(last.parent === activity.window.decorView)
            activity.finish()
        }
        waitFor("Activity fullscreen teardown missing") { activity.isDestroyed }
        runOnMainSync { check(last.parent == null && destroyedHidden == 1); client.onHideCustomView(); check(destroyedHidden == 1) }
        result.putString("stream", "PASS owned Android component boundary: eligible active notifications, quiet recovered content/intent/public redaction, one aggregate/remaining/unknown/empty; custom view decor/Back/duplicate/reentry/pause/destroy and bars restoration. Physical launcher and media playback remain separate.\n")
    }
    /** Same synthetic host and canonical UI as desktop, in the actual WebView.
     * The runner stages these two test-only files in the owned app's cache;
     * no production URL, permission or bridge is added or overridden. */
    private fun checkLatency(activity: MainActivity, result: Bundle) {
        check(latencySuite in setOf("history", "navigation", "settings", "unread", "actions", "topics", "groups", "compose", "media", "review", "outbox", "agents", "teams", "profile", "storage", "workspaces", "permissions")) { "Unknown timing workload." }
        val scriptFile = File(targetContext.cacheDir, "latency-fixture.mjs")
        val inputFile = File(targetContext.cacheDir, "latency-input.json")
        check(scriptFile.isFile && scriptFile.length() in 1L..262144L) { "Stage the bounded latency fixture in this owned app's private cache." }
        check(inputFile.isFile && inputFile.length() in 1L..1048576L) { "Stage the synthetic latency input in this owned app's private cache." }
        val source = scriptFile.readText()
        val entryImport = "import {mount,unmount} from '/assets/skins/comic/entry.mjs';"
        check(source.lineSequence().first() == entryImport) { "Unexpected latency fixture entry import." }
        val fixtureBody = source.substring(entryImport.length)
        check(!Regex("""(?m)^\s*(?:import|export)\b|\bimport\s*\.\s*meta\b""").containsMatchIn(fixtureBody)) {
            "The owned latency fixture contains another module-only construct."
        }
        val input = JSONObject(inputFile.readText())
        check(input.has("overview") && input.has("view")) { "Invalid synthetic latency input." }
        val field = MainActivity::class.java.getDeclaredField("web").apply { isAccessible = true }
        phase("latency-webview-before")
        val end = System.nanoTime() + TimeUnit.SECONDS.toNanos(30)
        var page: WebView? = null
        while (page == null && System.nanoTime() < end) {
            runOnMainSync { page = field.get(activity) as? WebView }
            if (page == null) Thread.sleep(100)
        }
        val web = checkNotNull(page) { "The native WebView did not open." }
        fun failureState(): JSONObject {
            val state = JSONObject()
            try {
                state.put("page", JSONObject(javascript(web, """
                    ({focused:document.hasFocus(),visibility:document.visibilityState,
                      nativeVisible:window.__agentnetAndroid?.visible?.()??null,
                      active:String(window.latencyProgress?.active||'').slice(0,160)})
                """.trimIndent())))
                phase("latency-native-focus-before")
                runOnMainSync {
                    state.put("activityWindowFocus", activity.hasWindowFocus())
                        .put("webViewFocus", web.hasFocus())
                }
                phase("latency-native-focus-after")
            } catch (e: Exception) {
                state.put("diagnosticUnavailable", e.javaClass.simpleName)
            }
            return state
        }
        phase("latency-webview-after")
        phase("latency-ready-before")
        var ready = false
        while (!ready && System.nanoTime() < end) {
            ready = javascript(web, "Boolean(window.__agentnetAndroid && document.readyState === 'complete')") == "true"
            if (!ready) Thread.sleep(100)
        }
        check(ready) { "The current authenticated local Go-host assets did not load." }
        phase("latency-ready-after")
        val startupFocus = JSONObject()
        if (latencySuite == "unread") {
            // Observe real startup ownership before any fixture DOM or click;
            // never request native or document focus from the instrumentation.
            var nativeFocused = false
            while (!nativeFocused && System.nanoTime() < end) {
                runOnMainSync { nativeFocused = activity.hasWindowFocus() && web.hasFocus() }
                if (!nativeFocused) Thread.sleep(25)
            }
            check(nativeFocused) { "Visible Activity did not give its document native focus" }
            check(javascript(web, "document.hasFocus() && document.visibilityState === 'visible' && window.__agentnetAndroid.visible()") == "true") {
                "Native document is not focused and visible before the unread journey"
            }
            if (android.os.Build.VERSION.SDK_INT >= 30) {
                var imeVisible: Boolean? = null
                runOnMainSync { imeVisible = activity.window.decorView.rootWindowInsets?.isVisible(android.view.WindowInsets.Type.ime()) }
                check(imeVisible == false) { "Initial native focus unexpectedly opened the keyboard or lacked insets" }
                startupFocus.put("imeVisible", imeVisible)
            }
            startupFocus.put("document", JSONObject(javascript(web, "({focused:document.hasFocus(),activeTag:document.activeElement?.tagName||''})")))
        }
        val saveAttempts = java.util.concurrent.atomic.AtomicInteger()
        val saveProof = java.util.concurrent.atomic.AtomicReference<JSONObject>()
        val stagedExport = java.util.concurrent.atomic.AtomicReference<File>()
        val savingField = MainActivity::class.java.getDeclaredField("saving").apply { isAccessible = true }
        val saveMonitor = if (latencySuite == "media") object : Instrumentation.ActivityMonitor() {
            override fun onStartActivity(intent: Intent): Instrumentation.ActivityResult? {
                if (intent.action != Intent.ACTION_CREATE_DOCUMENT) return null
                saveAttempts.incrementAndGet()
                val proof = JSONObject().put("action", intent.action)
                    .put("name", intent.getStringExtra(Intent.EXTRA_TITLE)).put("mime", intent.type)
                    .put("openable", intent.hasCategory(Intent.CATEGORY_OPENABLE))
                    .put("providerWrite", "not_executed")
                try {
                    val ready = checkNotNull(savingField.get(activity) as? ExportTransfer.Ready)
                    stagedExport.set(ready.file)
                    val expected = "# Fixture text preview\n\nExact private **original** text.".toByteArray(Charsets.UTF_8)
                    proof.put("stagedExactBytes", ready.file.length() == expected.size.toLong() && ready.file.readBytes().contentEquals(expected))
                        .put("stagedName", ready.name).put("stagedMime", ready.mime)
                } catch (e: Exception) { proof.put("captureError", e.javaClass.simpleName) }
                saveProof.set(proof)
                // Exercise the real native cancellation without opening a provider.
                return Instrumentation.ActivityResult(Activity.RESULT_CANCELED, null)
            }
        }.also { addMonitor(it) } else null
        try {
            phase("latency-setup-before")
            check(javascript(web, """
                (() => {
                  if (document.getElementById('fixture')) throw Error('Latency fixture already exists');
                  window.__agentnetLatencyPrevious = [...document.body.children].map(el => ({el,display:el.style.display,inert:el.inert}));
                  for (const {el} of window.__agentnetLatencyPrevious) {el.style.display='none';el.inert=true;}
                  const fixture=document.createElement('div');fixture.id='fixture';
                  fixture.style.cssText='position:fixed;inset:0;width:100%;height:100%;z-index:2147483647';
                  document.body.append(fixture);
                  window.__agentnetLatencyInput=$input;
                  window.__agentnetLatencyPlatform='android';
                  window.__agentnetLatencySuite=${JSONObject.quote(latencySuite)};
                  window.latencyReport=null;
                  return true;
                })()
            """.trimIndent()) == "true") { "The latency fixture setup did not start." }
            phase("latency-setup-after")
            phase("latency-launch-before")
            // evaluateJavascript is the existing test-only injection seam.
            // An inline module element would be refused by the product CSP.
            check(javascript(web, """
                (async () => {
                  'use strict';
                  const {mount,unmount}=await import(new URL('/assets/skins/comic/entry.mjs',location.href).href);
                  $fixtureBody
                })().catch(e=>{window.latencyReport={ok:false,error:'launch: '+String(e&&e.stack||e)}});
                true;
            """.trimIndent()) == "true") { "The latency fixture did not start (parse error)." }
            phase("latency-launch-after")
            phase("latency-report-before")
            val runEnd = System.nanoTime() + TimeUnit.SECONDS.toNanos(30)
            var report: JSONObject? = null
            while (report == null && System.nanoTime() < runEnd) {
                val raw = javascript(web, "window.latencyReport")
                if (raw != "null") report = JSONObject(raw)
                if (report == null) Thread.sleep(100)
            }
            if (report == null) result.putString("stream", "LATENCY_STATE_JSON=${failureState()}\n")
            val measured = checkNotNull(report) { "Native Android responsiveness fixture exceeded its existing 30-second bound." }
            if (latencySuite == "unread") measured.put("nativeStartupFocus", startupFocus)
            if (!measured.optBoolean("ok")) measured.put("androidFailureState", failureState())
            var nativeSaveValid = true
            if (saveMonitor != null) {
                var saveFinished = false
                while (!saveFinished && System.nanoTime() < runEnd) {
                    runOnMainSync { saveFinished = saveAttempts.get() == 1 && savingField.get(activity) == null }
                    saveFinished = saveFinished && stagedExport.get()?.exists() == false
                    if (!saveFinished) Thread.sleep(25)
                }
                val proof = saveProof.get() ?: JSONObject()
                nativeSaveValid = saveFinished && saveMonitor.hits == 1 && saveAttempts.get() == 1 &&
                    !proof.has("captureError") && proof.optBoolean("stagedExactBytes") &&
                    proof.optString("action") == Intent.ACTION_CREATE_DOCUMENT &&
                    proof.optString("name") == "fixture-preview.md" && proof.optBoolean("openable") &&
                    proof.optString("mime") == "application/octet-stream" &&
                    proof.optString("stagedName") == "fixture-preview.md" &&
                    proof.optString("stagedMime") == "application/octet-stream"
                measured.put("androidDownloadCancellation", proof.put("attempts", saveAttempts.get())
                    .put("monitorHits", saveMonitor.hits).put("cancelledAndTemporaryRemoved", saveFinished)
                    .put("ok", nativeSaveValid))
                if (!nativeSaveValid && measured.optBoolean("ok")) measured.put("ok", false)
                    .put("error", "Native download cancellation or original staged bytes were not proven")
            }
            measured.put("nativeOS", "Android").put("engine", "MainActivity WebView")
                .put("engineVersion", WebView.getCurrentWebViewPackage()?.versionName ?: "unknown")
                .put("sdk", android.os.Build.VERSION.SDK_INT).put("device", android.os.Build.MODEL)
            check(measured.toString().toByteArray().size <= 1048576) { "Latency result exceeds its bound." }
            result.putString("stream", "LATENCY_JSON=${measured}\n")
            phase("latency-report-after")
            check(measured.optBoolean("ok")) { measured.optString("error", "Native Android latency fixture failed") }
            check(nativeSaveValid) { "Native download cancellation or original staged bytes were not proven" }
        } finally {
            saveMonitor?.let { removeMonitor(it) }
            phase("latency-finally-before")
            javascript(web, """
                (() => {
                  window.__agentnetLatencyDispose?.();
                  document.getElementById('fixture')?.remove();document.getElementById('latency-test-script')?.remove();
                  for (const {el,display,inert} of window.__agentnetLatencyPrevious||[]) {el.style.display=display;el.inert=inert;}
                  delete window.__agentnetLatencyInput;delete window.__agentnetLatencyPlatform;delete window.__agentnetLatencySuite;delete window.__agentnetLatencyPrevious;
                  return true;
                })()
            """.trimIndent())
            scriptFile.delete(); inputFile.delete()
            phase("latency-finally-after")
        }
    }

    private fun javascript(page: WebView, script: String): String {
        val complete = CountDownLatch(1)
        var value = "null"
        phase("javascript-main-before")
        runOnMainSync { page.evaluateJavascript(script) { value = it; complete.countDown() } }
        phase("javascript-main-after")
        phase("javascript-result-before")
        check(complete.await(5, TimeUnit.SECONDS)) { "The owned WebView did not respond." }
        phase("javascript-result-after")
        return value
    }
    private fun snapshot(activity: MainActivity): String {
        val field = MainActivity::class.java.getDeclaredField("web").apply { isAccessible = true }
        phase("snapshot-webview-before")
        val deadline = System.nanoTime() + TimeUnit.SECONDS.toNanos(30)
        var page: WebView? = null
        while (page == null && System.nanoTime() < deadline) {
            runOnMainSync { page = field.get(activity) as? WebView }
            if (page == null) Thread.sleep(100)
        }
        val view = checkNotNull(page) { "The native host did not open." }
        phase("snapshot-webview-after")
        phase("snapshot-ready-before")
        var ready = false
        while (!ready && System.nanoTime() < deadline) {
            ready = javascript(view, "Boolean(window.__agentnetAndroid && document.readyState === 'complete')") == "true"
            if (!ready) Thread.sleep(100)
        }
        check(ready) { "The authenticated local page did not become ready." }
        phase("snapshot-ready-after")
        phase("snapshot-launch-before")
        javascript(view, """
            (function() {
              window.__agentnetUpdaterSnapshot = null;
              Promise.all([
                fetch('/api/overview').then(r => { if (!r.ok) throw Error('overview unavailable'); return r.json(); }),
                fetch('/api/thread?id=00000000000000000000000000000001').then(r => { if (!r.ok) throw Error('synthetic thread unavailable'); return r.json(); }),
                window.__agentnetNativeAppJSON('status', {})
              ]).then(([overview, thread, status]) => {
                const drafts = Object.fromEntries(Object.entries(localStorage).filter(([key]) => /draft/.test(key)).sort(([a],[b]) => a.localeCompare(b)));
                window.__agentnetUpdaterSnapshot = {
                  version: overview.version, installedVersion: status.version, updateResult: status.update_result || '',
                  preserved: {origin: location.origin, identity: overview.me,
                    messages: thread.messages.map(m => ({id:m.id, body:m.body, state:m.state ?? null, files:m.files ?? null})), drafts}
                };
              }).catch(error => { window.__agentnetUpdaterSnapshot = {error: String(error)}; });
              return true;
            })()
        """.trimIndent())
        phase("snapshot-launch-after")
        phase("snapshot-report-before")
        while (System.nanoTime() < deadline) {
            val encoded = javascript(view, "JSON.stringify(window.__agentnetUpdaterSnapshot)")
            if (encoded != "null" && encoded != "\"null\"") {
                val decoded = JSONObject("{\"value\":$encoded}").getString("value")
                val value = JSONObject(decoded)
                check(!value.has("error")) { value.optString("error") }
                val messageCount = value.getJSONObject("preserved").getJSONArray("messages").length()
                check(messageCount == 200) { "The owned 200-message fixture changed: actual=$messageCount." }
                phase("snapshot-report-after")
                return decoded
            }
            Thread.sleep(100)
        }
        error("The local fixture snapshot did not complete.")
    }
}
