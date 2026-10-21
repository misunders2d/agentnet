package io.github.misunders2d.agentnet

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.app.Service
import android.content.Intent
import android.content.pm.ServiceInfo
import android.os.Build
import android.os.IBinder

internal const val CONNECTION_CHANNEL = "connection"

internal fun connectionNotificationChannel() =
    NotificationChannel(CONNECTION_CHANNEL, "Background connection", NotificationManager.IMPORTANCE_LOW).apply { setShowBadge(false) }

internal fun connectionNotificationSettings(packageName: String) = Intent(android.provider.Settings.ACTION_CHANNEL_NOTIFICATION_SETTINGS)
    .putExtra(android.provider.Settings.EXTRA_APP_PACKAGE, packageName)
    .putExtra(android.provider.Settings.EXTRA_CHANNEL_ID, CONNECTION_CHANNEL)

/** User-started messaging continuity; never a polling worker or boot-started service. */
class ConnectionService : Service() {
    private val repo get() = (application as AgentNetApp).host
    override fun onCreate() {
        super.onCreate()
        getSystemService(NotificationManager::class.java).createNotificationChannel(
            connectionNotificationChannel())
    }
    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        if (intent?.action == "stop" || !repo.keepConnected()) {
            repo.setKeepConnected(false); repo.background(false); stopForeground(STOP_FOREGROUND_REMOVE); stopSelf(); return START_NOT_STICKY
        }
        val open = PendingIntent.getActivity(this, 0, Intent(this, MainActivity::class.java), PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT)
        val stop = PendingIntent.getService(this, 1, Intent(this, ConnectionService::class.java).setAction("stop"), PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT)
        val notification = Notification.Builder(this, CONNECTION_CHANNEL).setSmallIcon(R.drawable.ic_agentnet)
            .setContentTitle("AgentNet background connection")
            .setContentText("Keeping your chats connected. Tap to open.")
            .setVisibility(Notification.VISIBILITY_PRIVATE).setOngoing(true).setContentIntent(open)
            .addAction(Notification.Action.Builder(null, "Stop", stop).build()).build()
        try {
            if (Build.VERSION.SDK_INT >= 34) startForeground(1, notification, ServiceInfo.FOREGROUND_SERVICE_TYPE_REMOTE_MESSAGING)
            else startForeground(1, notification)
        } catch (_: Exception) {
            repo.setKeepConnected(false); repo.background(false); stopSelf()
            android.widget.Toast.makeText(this, "Android could not start AgentNet’s background connection. Try again from Connection settings.", android.widget.Toast.LENGTH_LONG).show()
            return START_NOT_STICKY
        }
        repo.background(true)
        return START_STICKY
    }
    override fun onDestroy() { repo.background(false); super.onDestroy() }
    override fun onBind(intent: Intent?): IBinder? = null
}
