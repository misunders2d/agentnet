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
import java.io.File
import java.util.concurrent.Executors

class AgentNetApp : Application() { val host by lazy { NativeHost(this) } }

data class HostState(val url: String = "", val origin: String = "", val error: String = "")

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
    var state = HostState(); private set
    fun observe(observer: (HostState) -> Unit) { observers.add(observer); observer(state); open() }
    fun remove(observer: (HostState) -> Unit) { observers.remove(observer) }
    fun keepConnected() = options.getBoolean("keep-connected", false)
    fun setKeepConnected(value: Boolean) { options.edit().putBoolean("keep-connected", value).apply() }
    fun foreground(value: Boolean) { foreground = value; if (value) open(); reconcile() }
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
                    override fun notify(workspace: String, fragment: String) { main.post { notifyActivity(workspace, fragment) } }
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
    private fun reconcile() = lane.execute {
        val app = core ?: return@execute
        try { if (foreground || background) app.start() else app.stop() }
        catch (_: Exception) { /* The page's existing connection state reports transport failures. */ }
    }
    private fun notifyActivity(workspace: String, fragment: String) {
        if (Build.VERSION.SDK_INT >= 33 && application.checkSelfPermission(Manifest.permission.POST_NOTIFICATIONS) != PackageManager.PERMISSION_GRANTED) return
        val route = WebPolicy.notificationDestination(workspace, fragment) ?: return
        val manager = application.getSystemService(NotificationManager::class.java)
        manager.createNotificationChannel(NotificationChannel("activity", "Chat activity", NotificationManager.IMPORTANCE_DEFAULT))
        val click = Intent(application, MainActivity::class.java).putExtra("destination", fragment).putExtra("workspace", workspace)
            .setData(android.net.Uri.Builder().scheme("agentnet-notification").authority("open").appendQueryParameter("workspace", workspace).appendQueryParameter("destination", fragment).build())
            .addFlags(Intent.FLAG_ACTIVITY_CLEAR_TOP or Intent.FLAG_ACTIVITY_SINGLE_TOP)
        val pending = PendingIntent.getActivity(application, route.hashCode(), click, PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT)
        val note = Notification.Builder(application, "activity").setSmallIcon(R.drawable.ic_agentnet)
            .setContentTitle("AgentNet").setContentText("New activity needs your attention.")
            .setVisibility(Notification.VISIBILITY_PRIVATE).setContentIntent(pending).setAutoCancel(true).build()
        manager.notify(route.hashCode(), note)
    }
}
