package io.github.misunders2d.agentnet

import android.Manifest
import android.app.Application
import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.Intent
import android.content.pm.PackageManager
import android.os.Build
import android.os.Handler
import android.os.Looper
import io.github.misunders2d.agentnet.core.core.App as CoreApp
import io.github.misunders2d.agentnet.core.core.Core
import io.github.misunders2d.agentnet.core.core.Notifier
import io.github.misunders2d.agentnet.core.core.UnreadSink
import java.io.File
import java.util.concurrent.Executors
import java.util.concurrent.atomic.AtomicBoolean
import org.json.JSONObject

class AgentNetApp : Application() { val host by lazy { NativeHost(this) } }

data class HostState(val url: String = "", val origin: String = "", val error: String = "")

internal const val ACTIVITY_CHANNEL = "activity"

internal fun activityNotificationChannel() = NotificationChannel(ACTIVITY_CHANNEL, "Chat activity", NotificationManager.IMPORTANCE_DEFAULT).apply {
    enableVibration(true)
    setSound(android.provider.Settings.System.DEFAULT_NOTIFICATION_URI, android.media.AudioAttributes.Builder()
        .setUsage(android.media.AudioAttributes.USAGE_NOTIFICATION).setContentType(android.media.AudioAttributes.CONTENT_TYPE_SONIFICATION).build())
    lockscreenVisibility = Notification.VISIBILITY_PRIVATE
}

// Read actual OS choices; a missing channel is distinct from a muted one.
internal fun activityNotificationStatus(appGranted: Boolean, channel: NotificationChannel?) = JSONObject()
    .put("granted", appGranted && channel?.importance != NotificationManager.IMPORTANCE_NONE)
    .put("app_granted", appGranted).apply {
        if (channel != null) put("channel", JSONObject().put("importance", channel.importance)
            .put("sound", channel.sound != null).put("vibration", channel.shouldVibrate()))
    }

internal fun activityNotificationSettings(packageName: String) = Intent(android.provider.Settings.ACTION_CHANNEL_NOTIFICATION_SETTINGS)
    .putExtra(android.provider.Settings.EXTRA_APP_PACKAGE, packageName)
    .putExtra(android.provider.Settings.EXTRA_CHANNEL_ID, ACTIVITY_CHANNEL)

internal fun startConnectionAfterConsent(options: android.content.SharedPreferences, granted: Boolean, start: () -> Unit) {
    if (!granted || options.contains("keep-connected")) return
    options.edit().putBoolean("keep-connected", true).apply()
    try { start() }
    catch (e: Exception) { options.edit().putBoolean("keep-connected", false).apply(); throw e }
}

/** Owns the embedded host once. UI data/events go directly through Comic's existing API. */
class NativeHost(private val application: Application) {
    private val main = Handler(Looper.getMainLooper())
    private val lane = Executors.newSingleThreadExecutor()
    private val options = application.getSharedPreferences("native-options", 0)
    private val observers = linkedSetOf<(HostState) -> Unit>()
    @Volatile private var foreground = false
    @Volatile private var background = false
    private var core: CoreApp? = null // lane only
    private var opening = false // main thread only
    private var runningWanted = false // lane only
    private val networkQueued = AtomicBoolean(false)
    private var unreadTotal = 0L // main thread; partial includes last-known contributions
    private var unreadPartial = true
    private val networkMonitor = NetworkMonitor(application, main) { networkChanged() }
    var state = HostState(); private set
    fun observe(observer: (HostState) -> Unit) { observers.add(observer); observer(state); open() }
    fun remove(observer: (HostState) -> Unit) { observers.remove(observer) }
    fun keepConnected() = options.getBoolean("keep-connected", false)
    fun setKeepConnected(value: Boolean) { options.edit().putBoolean("keep-connected", value).apply() }
    // First consent uses the existing visible foreground service. A saved Stop
    // or settings choice always wins, including after upgrades and restarts.
    fun afterNotificationConsent(granted: Boolean, start: () -> Unit) {
        startConnectionAfterConsent(options, granted, start)
    }
    fun foreground(value: Boolean) { val resumed = value && !foreground; foreground = value; if (value) open(); reconcile(resumed) }
    fun background(value: Boolean) { background = value; if (value) open(); reconcile() }
    private fun publish(next: HostState) = main.post { state = next; observers.toList().forEach { it(next) } }
    fun open() {
        if (opening || state.url.isNotEmpty()) return
        opening = true
        lane.execute {
            try {
                val app = Core.openApp(File(application.noBackupFilesDir, "agentnet").absolutePath, options.getInt("web-port", 0).toLong())
                if (!options.edit().putInt("web-port", app.port().toInt()).commit()) {
                    app.close(); throw IllegalStateException("Cannot preserve this app's local web address.")
                }
                app.setNotifier(object : Notifier {
                    override fun notify(workspace: String, fragment: String, title: String, body: String) { main.post { notifyActivity(workspace, fragment, title, body) } }
                    override fun dismiss(workspace: String, fragment: String) { main.post { dismissActivity(workspace, fragment) } }
                })
                app.setUnreadSink(object : UnreadSink {
                    override fun unread(total: Long, partial: Boolean) { main.post {
                        unreadTotal = total.coerceAtLeast(0); unreadPartial = partial
                        updateActivityBadges(application, unreadTotal, unreadPartial)
                    } }
                })
                core = app
                publish(HostState(app.url(), app.origin()))
                reconcile()
            } catch (_: Exception) {
                // Never expose the authenticated URL or private enrollment material in diagnostics.
                publish(HostState(error = "AgentNet could not open its saved app. The saved local port may be occupied. Close another instance and try again; your data has been kept."))
            } finally { main.post { opening = false } }
        }
    }
    private fun networkChanged() {
        if (!networkQueued.compareAndSet(false, true)) return
        lane.execute {
            networkQueued.set(false)
            if (foreground || background) core?.networkChanged()
        }
    }
    private fun reconcile(resumed: Boolean = false) = lane.execute {
        val app = core ?: return@execute
        try {
            val wanted = foreground || background
            if (wanted) {
                if (resumed && runningWanted) app.networkChanged()
                app.start()
            } else app.stop()
            runningWanted = wanted
        }
        catch (_: Exception) { /* The page's existing connection state reports transport failures. */ }
    }
    private fun notificationAppPermitted(): Boolean {
        if (Build.VERSION.SDK_INT >= 33 && application.checkSelfPermission(Manifest.permission.POST_NOTIFICATIONS) != PackageManager.PERMISSION_GRANTED) return false
        val manager = application.getSystemService(NotificationManager::class.java)
        return manager.areNotificationsEnabled()
    }
    fun notificationStatus(): JSONObject = activityNotificationStatus(notificationAppPermitted(),
        application.getSystemService(NotificationManager::class.java).getNotificationChannel(ACTIVITY_CHANNEL))
    fun notificationsPermitted(): Boolean = notificationStatus().getBoolean("granted")
    fun prepareNotificationChannel() {
        application.getSystemService(NotificationManager::class.java).createNotificationChannel(activityNotificationChannel())
    }
    private fun dismissActivity(workspace: String, fragment: String) {
        WebPolicy.notificationTag(workspace, fragment)?.let { tag ->
            application.getSystemService(NotificationManager::class.java).cancel(tag, 0)
            updateActivityBadges(application, unreadTotal, unreadPartial)
        }
    }
    private fun notifyActivity(workspace: String, fragment: String, title: String, body: String) {
        if (!notificationsPermitted()) return
        val tag = WebPolicy.notificationTag(workspace, fragment) ?: return
        val manager = application.getSystemService(NotificationManager::class.java)
        // Defaults apply to a newly created channel only. Reusing its id leaves
        // existing sound/vibration/mute/DND choices under Android's authority.
        prepareNotificationChannel()
        val click = Intent(application, MainActivity::class.java).putExtra("destination", fragment).putExtra("workspace", workspace)
            .setData(android.net.Uri.Builder().scheme("agentnet-notification").authority("open").appendQueryParameter("workspace", workspace).appendQueryParameter("destination", fragment).build())
            .addFlags(Intent.FLAG_ACTIVITY_CLEAR_TOP or Intent.FLAG_ACTIVITY_SINGLE_TOP)
        val pending = PendingIntent.getActivity(application, 0, click, PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT)
        val redacted = Notification.Builder(application, ACTIVITY_CHANNEL).setSmallIcon(R.drawable.ic_agentnet)
            .setContentTitle("AgentNet").setContentText("New activity needs your attention.")
            .setVisibility(Notification.VISIBILITY_PUBLIC).build()
        val note = Notification.Builder(application, ACTIVITY_CHANNEL).setSmallIcon(R.drawable.ic_agentnet)
            .setContentTitle(title.ifBlank { "AgentNet" }).setContentText(body)
            .setStyle(Notification.BigTextStyle().bigText(body)).setCategory(Notification.CATEGORY_MESSAGE)
            .setVisibility(Notification.VISIBILITY_PRIVATE).setPublicVersion(redacted)
            .setContentIntent(pending).setAutoCancel(true)
            .setNumber(activityBadgeNumber(unreadTotal, unreadPartial)).build()
        manager.notify(tag, 0, note)
        updateActivityBadges(application, unreadTotal, unreadPartial)
    }
}

// Android owns launcher appearance. Never create a notification just for a dot.
// Recovering the OS object preserves its private content, exact click intent,
// auto-cancel and public redaction without retaining a second plaintext body map.
internal fun updateActivityBadges(application: Application, total: Long, partial: Boolean) {
    val manager = application.getSystemService(NotificationManager::class.java)
    val active = manager.activeNotifications.filter {
        it.notification.channelId == ACTIVITY_CHANNEL && activityBadgeTag(it.tag)
    }.sortedWith(compareByDescending<android.service.notification.StatusBarNotification> { it.postTime }.thenBy { it.key })
    val number = activityBadgeNumber(total, partial)
    active.forEachIndexed { index, entry ->
        val desired = if (index == 0) number else 0
        if (entry.notification.number != desired) {
            val note = Notification.Builder.recoverBuilder(application, entry.notification)
                .setNumber(desired).setOnlyAlertOnce(true).build()
            manager.notify(entry.tag, entry.id, note)
        }
    }
}

internal fun activityBadgeTag(tag: String?): Boolean {
    if (tag == null || tag.length > 2048) return false
    val workspace = tag.split('&').firstOrNull { it.startsWith("workspace=") }?.substringAfter('=') ?: return false
    return WebPolicy.notificationTag(workspace, tag) == tag
}

// A partial sum can retain stale contributions; it is not an exact count.
internal fun activityBadgeNumber(total: Long, partial: Boolean): Int =
    if (partial) 0 else total.coerceIn(0, Int.MAX_VALUE.toLong()).toInt()
