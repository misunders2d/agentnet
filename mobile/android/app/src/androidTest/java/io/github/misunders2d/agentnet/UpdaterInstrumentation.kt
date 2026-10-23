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
    private var ownedLatencyFixture = false
    override fun onCreate(arguments: Bundle?) {
        super.onCreate(arguments)
        mode = arguments?.getString("mode") ?: "install"
        latencySuite = arguments?.getString("latencySuite") ?: "history"
        ownedLatencyFixture = arguments?.getString("ownedFixture") == "1"
        start()
    }
    override fun onStart() {
        val result = Bundle()
        try {
            check(mode in setOf("install", "snapshot", "network", "cleanup", "interrupt", "notifications", "notifications-denied", "indicators-video", "latency")) { "Unknown fixture operation." }
            check(mode != "latency" || ownedLatencyFixture) { "Latency mode requires the explicitly owned disposable fixture runner." }
            if (mode == "cleanup") {
                val preferences=targetContext.applicationContext.getSharedPreferences("native-update",0)
                val id=preferences.getInt("session",-1)
                val installer=targetContext.packageManager.packageInstaller
                val info=installer.getSessionInfo(id)
                check(info?.appPackageName == targetContext.packageName && info.installerPackageName == targetContext.packageName) { "No test-owned fixture session exists." }
                installer.abandonSession(id)
                preferences.edit().remove("session").remove("result").commit()
                result.putString("stream","Discarded test-owned session blocked by harness teardown; this is setup cleanup, not cancellation evidence.\n")
                finish(Activity.RESULT_OK,result); return
            }
            val activity = startActivitySync(Intent(targetContext, MainActivity::class.java).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)) as MainActivity
            waitForIdleSync()
            if (mode == "latency") {
                checkLatency(activity, result)
                finish(Activity.RESULT_OK, result); return
            }
            if (mode == "indicators-video") {
                checkIndicatorsVideo(activity, result)
                finish(Activity.RESULT_OK, result); return
            }
            if (mode.startsWith("notifications")) {
                checkNotifications(activity, mode == "notifications-denied", result)
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
                    finish(Activity.RESULT_OK,result); return
                }
                // Finishing instrumentation kills the target process. Keep it visible
                // until Android's real callback, or normal package replacement.
                check(terminal.await(120, TimeUnit.SECONDS)) { "Android installation confirmation did not reach a terminal result." }
                preferences.unregisterOnSharedPreferenceChangeListener(listener)
                result.putString("stream", "Real installer callback: ${preferences.getString("result", "")}\n")
            }
            finish(Activity.RESULT_OK, result)
        } catch (e: Exception) {
            result.putString("stream", "Updater fixture failed: ${e.message}\n")
            finish(Activity.RESULT_CANCELED, result)
        }
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
        val post = NativeHost::class.java.getDeclaredMethod("notifyActivity", String::class.java, String::class.java, String::class.java, String::class.java).apply { isAccessible = true }
        val dismiss = NativeHost::class.java.getDeclaredMethod("dismissActivity", String::class.java, String::class.java).apply { isAccessible = true }
        val workspace = "default"
        val routes = listOf("conv=" + "a".repeat(64), "review", "msg=" + "b".repeat(32) + "&dir=in")
        val tags = routes.map { WebPolicy.notificationDestination(workspace, it)!! }.toSet()
        fun active() = manager.activeNotifications.filter { it.tag in tags }
        fun emit(fragment: String) = runOnMainSync { post.invoke(host, workspace, fragment, "Fixture sender", "Locally decrypted fixture preview") }
        fun waitFor(label: String, predicate: () -> Boolean) {
            val end = System.nanoTime() + TimeUnit.SECONDS.toNanos(5)
            while (!predicate() && System.nanoTime() < end) Thread.sleep(50)
            check(predicate()) { label }
        }
        val totalField = NativeHost::class.java.getDeclaredField("unreadTotal").apply { isAccessible = true }
        val partialField = NativeHost::class.java.getDeclaredField("unreadPartial").apply { isAccessible = true }
        val dismissedField = NativeHost::class.java.getDeclaredField("dismissedActivityTags").apply { isAccessible = true }
        @Suppress("UNCHECKED_CAST")
        fun pendingTags() = dismissedField.get(host) as MutableSet<String> // main-thread calls only
        var savedTotal = 0L; var savedPartial = true; var restoreBadges = false
        val barrierTag = "fixture-badge-barrier"
        var barrier = 0
        try {
            check(host.notificationsPermitted() != denied) { "Expected Android permission ${if (denied) "denied" else "granted"}; this test never changes it." }
            routes.forEach { route -> runOnMainSync { dismiss.invoke(host, workspace, route) } }
            if (denied) {
                routes.forEach(::emit)
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
            waitFor("Foreground notification/dedup failed") { active().size == 1 }
            runOnMainSync { activity.moveTaskToBack(true) }
            emit(routes[1])
            waitFor("Background OK notification missing") { active().size == 2 }
            uiAutomation.executeShellCommand("input keyevent 223").close()
            emit(routes[2])
            waitFor("Screen-off message/reminder route missing") { active().size == 3 }
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
            runOnMainSync { dismiss.invoke(host, workspace, routes[0]) }
            waitFor("Reading exact chat did not dismiss its notification") { active().none { it.tag == WebPolicy.notificationDestination(workspace, routes[0]) } }
            check(active().any { it.tag == WebPolicy.notificationDestination(workspace, routes[1]) }) { "Reading another chat cleared an OK" }
            // Reuse this mode's real NativeHost/NotificationManager with positive
            // counts. The private set models a cancel still visible in an OS
            // snapshot; it is not a substitute for the actual cancel below.
            emit(routes[1]); emit(routes[2]); emit(routes[0])
            waitFor("Positive badge fixture notices missing") { active().size == 3 }
            fun refreshUnread(total: Long) = runOnMainSync {
                totalField.setLong(host, total); partialField.setBoolean(host, false)
                updateActivityBadges(app, total, false, pendingTags()) // same projection as UnreadSink
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
            val settled = active().associate { it.key to (it.postTime to it.notification.number) }
            refreshUnread(7); settleBadgePosts()
            check(active().associate { it.key to (it.postTime to it.notification.number) } == settled) {
                "Same unread total unnecessarily reposted notifications"
            }
            val cancelledTag = WebPolicy.notificationTag(workspace, routes[0])!!
            val beforeExcluded = active().single { it.tag == cancelledTag }
            runOnMainSync { pendingTags().add(cancelledTag) }
            refreshUnread(5); settleBadgePosts()
            waitFor("Healthy notice lost its count during pending cancellation") {
                active().count { it.tag != cancelledTag && it.notification.number == 5 } == 1
            }
            val excluded = active().single { it.tag == cancelledTag }
            check(excluded.postTime == beforeExcluded.postTime && excluded.notification.number == beforeExcluded.notification.number) {
                "Still-active cancelled tag was reposted by unread update"
            }
            runOnMainSync {
                dismiss.invoke(host, workspace, routes[0])
                totalField.setLong(host, 4L); updateActivityBadges(app, 4, false, pendingTags())
                dismiss.invoke(host, workspace, routes[1])
                totalField.setLong(host, 3L); updateActivityBadges(app, 3, false, pendingTags())
            }
            settleBadgePosts()
            val survivorTag = WebPolicy.notificationTag(workspace, routes[2])!!
            waitFor("Unread or second cancel resurrected a read notification") {
                active().singleOrNull()?.let { it.tag == survivorTag && it.notification.number == 3 } == true
            }
            runOnMainSync { post.invoke(host, workspace, routes[0], "Fresh fixture sender", "New unseen fixture preview") }
            waitFor("New activity for a read chat was suppressed") {
                active().any { it.tag == cancelledTag && it.notification.extras.getCharSequence(android.app.Notification.EXTRA_TEXT).toString() == "New unseen fixture preview" }
            }
            runOnMainSync { check(cancelledTag !in pendingTags()) { "New arrival retained pending cancellation" } }
            result.putString("stream", "PASS Android OS notification boundary: foreground/dedup, background OK, screen-off message/reminder route, private content, exact tap/read dismissal, positive pending-cancel/unread/second-cancel/new-arrival and settled same-total badge checks. No physical-phone or relay delivery claim.\n")
        } finally {
            uiAutomation.executeShellCommand("input keyevent 224").close()
            routes.forEach { route -> runOnMainSync { dismiss.invoke(host, workspace, route) } }
            (1..barrier).forEach { manager.cancel("$barrierTag-$it", 83) }
            if (restoreBadges) runOnMainSync {
                totalField.setLong(host, savedTotal); partialField.setBoolean(host, savedPartial)
                updateActivityBadges(app, savedTotal, savedPartial, pendingTags())
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
        fun waitFor(label: String, predicate: () -> Boolean) {
            val end = System.nanoTime() + TimeUnit.SECONDS.toNanos(5)
            while (!predicate() && System.nanoTime() < end) Thread.sleep(25)
            check(predicate()) { label }
        }
        fun eligible() = manager.activeNotifications.filter { it.notification.channelId == ACTIVITY_CHANNEL && activityBadgeTag(it.tag) }
        check(eligible().isEmpty()) { "Use the isolated fixture without pre-existing activity notifications." }
        val tags = listOf("1".repeat(32), "2".repeat(32)).map { WebPolicy.notificationTag("default", "msg=$it&dir=in")!! }
        val click = android.app.PendingIntent.getActivity(app, 71, Intent(app, MainActivity::class.java).setAction("fixture-indicator"), android.app.PendingIntent.FLAG_IMMUTABLE)
        val redacted = android.app.Notification.Builder(app, ACTIVITY_CHANNEL).setSmallIcon(R.drawable.ic_agentnet)
            .setContentTitle("AgentNet").setContentText("New activity").build()
        fun note() = android.app.Notification.Builder(app, ACTIVITY_CHANNEL).setSmallIcon(R.drawable.ic_agentnet)
            .setContentTitle("Fixture sender").setContentText("Private retained preview")
            .setStyle(android.app.Notification.BigTextStyle().bigText("Private retained preview"))
            .setContentIntent(click).setAutoCancel(true).setVisibility(android.app.Notification.VISIBILITY_PRIVATE)
            .setPublicVersion(redacted).build()
        try {
            runOnMainSync { updateActivityBadges(app, 9, false) }
            check(eligible().isEmpty()) { "Badge-only notification appeared" }
            tags.forEachIndexed { index, tag -> manager.notify(tag, index + 71, note()) }
            manager.notify("unrelated-tag", 73, note())
            waitFor("Fixture notifications not active") { eligible().size == 2 && manager.activeNotifications.any { it.tag == "unrelated-tag" } }
            val before = eligible().associateBy { it.tag }
            val newestKey = before.values.sortedWith(compareByDescending<android.service.notification.StatusBarNotification> { it.notification.`when` }.thenBy { it.key }).first().key
            runOnMainSync { updateActivityBadges(app, 7, false) }
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
                if (now.number == 7) check(now.flags and android.app.Notification.FLAG_ONLY_ALERT_ONCE != 0)
            }
            check(manager.activeNotifications.single { it.tag == "unrelated-tag" }.notification.number == 0)
            runOnMainSync { updateActivityBadges(app, 5, true) }
            waitFor("Partial positive estimate did not clear numeric badge") { eligible().all { it.notification.number == 0 } }
            runOnMainSync { updateActivityBadges(app, 0, true) }
            check(eligible().all { it.notification.number == 0 })
            runOnMainSync { updateActivityBadges(app, 5, false) }
            waitFor("Exact aggregate not restored") { eligible().count { it.notification.number == 5 } == 1 }
            val numbered = eligible().single { it.notification.number == 5 }
            manager.cancel(numbered.tag, numbered.id)
            waitFor("Exact dismissal missing") { eligible().size == 1 }
            runOnMainSync { updateActivityBadges(app, 2, false) }
            waitFor("Remaining notification count missing") { eligible().single().notification.number == 2 }
            runOnMainSync { updateActivityBadges(app, 0, true) }
            waitFor("Unknown numeric badge not cleared") { eligible().all { it.notification.number == 0 } }
        } finally {
            tags.forEachIndexed { index, tag -> manager.cancel(tag, index + 71) }
            manager.cancel("unrelated-tag", 73)
        }
        waitFor("Fixture badge cleanup") { eligible().isEmpty() }
        runOnMainSync { updateActivityBadges(app, 12, false) }
        check(eligible().isEmpty())
        // A newly posted ordinary activity notification uses the latest scalar.
        val totalField = NativeHost::class.java.getDeclaredField("unreadTotal").apply { isAccessible = true }
        val partialField = NativeHost::class.java.getDeclaredField("unreadPartial").apply { isAccessible = true }
        val post = NativeHost::class.java.getDeclaredMethod("notifyActivity", String::class.java, String::class.java, String::class.java, String::class.java).apply { isAccessible = true }
        val dismiss = NativeHost::class.java.getDeclaredMethod("dismissActivity", String::class.java, String::class.java).apply { isAccessible = true }
        val freshRoute = "msg=" + "3".repeat(32) + "&dir=in"
        var savedTotal = 0L; var savedPartial = true
        try {
            runOnMainSync {
                savedTotal = totalField.getLong(app.host); savedPartial = partialField.getBoolean(app.host)
                totalField.setLong(app.host, 6L); partialField.setBoolean(app.host, false)
                post.invoke(app.host, "default", freshRoute, "Fresh fixture sender", "Fresh private body")
            }
            waitFor("New activity did not use latest aggregate") { eligible().singleOrNull()?.notification?.number == 6 }
            runOnMainSync {
                partialField.setBoolean(app.host, true)
                post.invoke(app.host, "default", freshRoute, "Fresh fixture sender", "Fresh private body")
            }
            waitFor("New activity exposed partial positive estimate as an exact number") { eligible().singleOrNull()?.notification?.number == 0 }
        } finally {
            runOnMainSync {
                dismiss.invoke(app.host, "default", freshRoute)
                totalField.setLong(app.host, savedTotal); partialField.setBoolean(app.host, savedPartial)
            }
        }
        waitFor("Fresh activity cleanup") { eligible().isEmpty() }

        val field = MainActivity::class.java.getDeclaredField("web").apply { isAccessible = true }
        var page: WebView? = null
        waitFor("Native WebView not ready") { runOnMainSync { page = field.get(activity) as? WebView }; page != null }
        val web = checkNotNull(page)
        waitFor("Native WebView not loaded") { var ready = false; runOnMainSync { ready = web.url?.startsWith(app.host.state.origin) == true }; ready }
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
        val end = System.nanoTime() + TimeUnit.SECONDS.toNanos(30)
        var page: WebView? = null
        while (page == null && System.nanoTime() < end) {
            runOnMainSync { page = field.get(activity) as? WebView }
            if (page == null) Thread.sleep(100)
        }
        val web = checkNotNull(page) { "The native WebView did not open." }
        var ready = false
        while (!ready && System.nanoTime() < end) {
            ready = javascript(web, "Boolean(window.__agentnetAndroid && document.readyState === 'complete')") == "true"
            if (!ready) Thread.sleep(100)
        }
        check(ready) { "The current authenticated local Go-host assets did not load." }
        try {
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
            val runEnd = System.nanoTime() + TimeUnit.SECONDS.toNanos(30)
            var report: JSONObject? = null
            while (report == null && System.nanoTime() < runEnd) {
                val raw = javascript(web, "window.latencyReport")
                if (raw != "null") report = JSONObject(raw)
                if (report == null) Thread.sleep(100)
            }
            val measured = checkNotNull(report) { "Native Android responsiveness fixture exceeded its existing 30-second bound." }
            measured.put("nativeOS", "Android").put("engine", "MainActivity WebView")
                .put("engineVersion", WebView.getCurrentWebViewPackage()?.versionName ?: "unknown")
                .put("sdk", android.os.Build.VERSION.SDK_INT).put("device", android.os.Build.MODEL)
            check(measured.toString().toByteArray().size <= 1048576) { "Latency result exceeds its bound." }
            result.putString("stream", "LATENCY_JSON=${measured}\n")
            check(measured.optBoolean("ok")) { measured.optString("error", "Native Android latency fixture failed") }
        } finally {
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
        }
    }

    private fun javascript(page: WebView, script: String): String {
        val complete = CountDownLatch(1)
        var value = "null"
        runOnMainSync { page.evaluateJavascript(script) { value = it; complete.countDown() } }
        check(complete.await(5, TimeUnit.SECONDS)) { "The owned WebView did not respond." }
        return value
    }
    private fun snapshot(activity: MainActivity): String {
        val field = MainActivity::class.java.getDeclaredField("web").apply { isAccessible = true }
        val deadline = System.nanoTime() + TimeUnit.SECONDS.toNanos(30)
        var page: WebView? = null
        while (page == null && System.nanoTime() < deadline) {
            runOnMainSync { page = field.get(activity) as? WebView }
            if (page == null) Thread.sleep(100)
        }
        val view = checkNotNull(page) { "The native host did not open." }
        var ready = false
        while (!ready && System.nanoTime() < deadline) {
            ready = javascript(view, "Boolean(window.__agentnetAndroid && document.readyState === 'complete')") == "true"
            if (!ready) Thread.sleep(100)
        }
        check(ready) { "The authenticated local page did not become ready." }
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
        while (System.nanoTime() < deadline) {
            val encoded = javascript(view, "JSON.stringify(window.__agentnetUpdaterSnapshot)")
            if (encoded != "null" && encoded != "\"null\"") {
                val decoded = JSONObject("{\"value\":$encoded}").getString("value")
                val value = JSONObject(decoded)
                check(!value.has("error")) { value.optString("error") }
                val messageCount = value.getJSONObject("preserved").getJSONArray("messages").length()
                check(messageCount == 200) { "The owned 200-message fixture changed: actual=$messageCount." }
                return decoded
            }
            Thread.sleep(100)
        }
        error("The local fixture snapshot did not complete.")
    }
}
