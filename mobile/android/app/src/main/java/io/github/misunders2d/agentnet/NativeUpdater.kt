package io.github.misunders2d.agentnet

import android.app.PendingIntent
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.content.pm.PackageInstaller
import android.content.pm.PackageManager
import android.os.Build
import org.json.JSONObject
import java.io.File
import java.net.HttpURLConnection
import java.net.URL
import java.security.MessageDigest

/** One fixed-origin update, verified before entering Android's confirmation flow. */
internal object UpdateState { val lock = Any() }

class NativeUpdater(private val context: Context) {
    data class Prepared(val file: File, val version: String)
    private val preferences get() = context.applicationContext.getSharedPreferences("native-update", Context.MODE_PRIVATE)
    private fun sha(bytes: ByteArray) = MessageDigest.getInstance("SHA-256").digest(bytes).joinToString("") { "%02x".format(it) }
    fun status() = JSONObject().put("version", BuildConfig.UPDATE_VERSION).put("app_update_supported", true)
        .put("cli_path", "").put("cli_state", "custom").put("update_result", preferences.getString("result", ""))
    private fun connection(start: String, deadline: Long): Pair<HttpURLConnection, String> {
        var url = start
        repeat(6) {
            check(System.nanoTime() < deadline) { "The update request timed out. Try again." }
            require(UpdatePolicy.redirect(url)) { "The update redirected outside the official release hosts." }
            val c = URL(url).openConnection() as HttpURLConnection
            c.instanceFollowRedirects = false; c.connectTimeout = 15_000; c.readTimeout = 20_000
            c.setRequestProperty("Accept", "application/octet-stream"); c.setRequestProperty("User-Agent", "AgentNet-Android")
            val status = try { c.responseCode } catch (e: Exception) {
                c.disconnect(); throw UpdatePolicy.unreachable(url, e)?.let { IllegalStateException(it, e) } ?: e
            }
            if (status in listOf(301,302,303,307,308)) {
                val next = c.getHeaderField("Location"); c.disconnect()
                require(!next.isNullOrEmpty()) { "The release redirect is missing." }
                url = URL(URL(url), next).toString()
            } else {
                if (status != 200) {
                    c.disconnect()
                    if (status == 404 && (start.endsWith("/${UpdatePolicy.asset}") || start.endsWith("/SHA256SUMS")))
                        error("Android update files for this release are not published yet. Try again later.")
                    error("The official release could not be downloaded (HTTP $status). Try again.")
                }
                return c to url
            }
        }
        error("The release redirected too many times.")
    }
    private fun read(url: String, limit: Long, destination: File? = null): Pair<ByteArray, String> {
        val deadline = System.nanoTime() + 180_000_000_000L
        val (c, finalURL) = connection(url, deadline)
        try {
            require(c.contentLengthLong <= limit) { "The release download exceeds its size limit." }
            val memory = if (destination == null) java.io.ByteArrayOutputStream() else null
            val output = destination?.outputStream() ?: memory!!
            var size = 0L
            output.use { out -> c.inputStream.use { input ->
                val buffer = ByteArray(32 * 1024)
                while (true) {
                    check(System.nanoTime() < deadline) { "The update download timed out. Try again." }
                    val count = input.read(buffer); if (count == -1) break
                    size += count; require(size <= limit) { "The release download exceeds its size limit." }
                    out.write(buffer, 0, count)
                }
            } }
            require(c.contentLengthLong < 0 || size == c.contentLengthLong) { "The release download was incomplete. Try again." }
            return (memory?.toByteArray() ?: byteArrayOf()) to finalURL
        } finally { c.disconnect() }
    }
    fun check(): JSONObject {
        val (response, url) = connection("${UpdatePolicy.repository}/releases/latest", System.nanoTime() + 30_000_000_000L)
        response.disconnect()
        val latest = UpdatePolicy.latest(url)
        val current = UpdatePolicy.code(BuildConfig.UPDATE_VERSION)
        val next = UpdatePolicy.code(latest)
        return JSONObject().put("version", BuildConfig.UPDATE_VERSION).put("latest", latest)
            .put("state", if (next > current) "available" else if (next == current) "current" else "ahead")
    }
    /** A new attempt replaces the last attempt's outcome, so an old "updated" never sits beside a new failure. */
    fun begin() { preferences.edit().remove("result").apply() }
    fun prepare(): Prepared {
        val latest = check().getString("latest")
        require(UpdatePolicy.code(latest) > UpdatePolicy.code(BuildConfig.UPDATE_VERSION)) { "This app already has the latest release." }
        val sums = read(UpdatePolicy.downloadURL(latest, "SHA256SUMS"), 1024 * 1024).first.toString(Charsets.UTF_8)
        val expected = UpdatePolicy.checksum(sums)
        // Each Activity attempt owns its file; an old callback cannot delete a new attempt.
        context.cacheDir.listFiles()?.filter {
            it.name.startsWith("agentnet-update-") && it.name.endsWith(".apk") && System.currentTimeMillis() - it.lastModified() > 24 * 60 * 60 * 1000L
        }?.forEach { it.delete() }
        val file = UpdatePolicy.attemptFile(context.cacheDir)
        try {
            read(UpdatePolicy.downloadURL(latest, UpdatePolicy.asset), UpdatePolicy.maxApk, file)
            val digest = MessageDigest.getInstance("SHA-256")
            file.inputStream().use { input -> val buffer = ByteArray(32*1024); while (true) { val n=input.read(buffer); if(n<0)break;digest.update(buffer,0,n) } }
            require(digest.digest().joinToString("") { "%02x".format(it) } == expected) { "The release checksum does not match. No update was installed." }
            verify(file, latest)
            return Prepared(file, latest)
        } catch (e: Exception) { file.delete(); throw e }
    }
    @Suppress("DEPRECATION")
    private fun verify(file: File, version: String) {
        val flags = if (Build.VERSION.SDK_INT >= 28) PackageManager.GET_SIGNING_CERTIFICATES else PackageManager.GET_SIGNATURES
        val pm=context.packageManager
        val archive=pm.getPackageArchiveInfo(file.absolutePath, flags) ?: error("The release APK is invalid.")
        val installed=pm.getPackageInfo(context.packageName, flags)
        fun certificates(info: android.content.pm.PackageInfo): List<String> =
            (if(Build.VERSION.SDK_INT >= 28) info.signingInfo?.apkContentsSigners else info.signatures)?.map { sha(it.toByteArray()) } ?: emptyList()
        val next=if(Build.VERSION.SDK_INT >=28)archive.longVersionCode else archive.versionCode.toLong()
        val current=if(Build.VERSION.SDK_INT >=28)installed.longVersionCode else installed.versionCode.toLong()
        UpdatePolicy.validateApk(archive.packageName, context.packageName, certificates(archive), certificates(installed), next, current, archive.versionName, version)
    }
    fun install(prepared: Prepared) {
        verify(prepared.file, prepared.version)
        val installer=context.packageManager.packageInstaller
        val id = synchronized(UpdateState.lock) {
            val active=preferences.getInt("session",-1)
            val previous=if(active<0) null else installer.getSessionInfo(active)
            if(previous != null) {
                require(UpdatePolicy.replacePendingSession(previous.installerPackageName, previous.appPackageName,
                    context.packageName, preferences.getBoolean("pending_confirmation",false), previous.isActive)) {
                    "Android is still working on this update. Finish its installation before trying again."
                }
                // Invalidate before abandonment: its delayed callback cannot own a retry.
                check(preferences.edit().remove("session").remove("pending_confirmation").commit())
                installer.abandonSession(active)
            }
            val params=PackageInstaller.SessionParams(PackageInstaller.SessionParams.MODE_FULL_INSTALL).apply {
                setAppPackageName(context.packageName)
                setSize(prepared.file.length())
                if(Build.VERSION.SDK_INT >=31)setRequireUserAction(PackageInstaller.SessionParams.USER_ACTION_REQUIRED)
            }
            val created=installer.createSession(params)
            try {
                check(preferences.edit().putInt("session",created).putBoolean("pending_confirmation",false)
                    .putString("result","Preparing Android installation confirmation.").commit())
            } catch(e:Exception) {
                installer.abandonSession(created)
                prepared.file.delete()
                throw e
            }
            created
        }
        try {
            installer.openSession(id).use { session ->
                // APK I/O is outside the callback state lock.
                session.openWrite("AgentNet.apk",0,prepared.file.length()).use { out -> prepared.file.inputStream().use { it.copyTo(out) };session.fsync(out) }
                val callback=Intent(context,UpdateResultReceiver::class.java).setAction("${context.packageName}.UPDATE_RESULT")
                val flags=PendingIntent.FLAG_UPDATE_CURRENT or if(Build.VERSION.SDK_INT >=31)PendingIntent.FLAG_MUTABLE else 0
                val result=PendingIntent.getBroadcast(context,id,callback,flags)
                session.commit(result.intentSender)
            }
        } catch(e:Exception) {
            synchronized(UpdateState.lock) {
                if(preferences.getInt("session",-1)==id)
                    check(preferences.edit().remove("session").remove("pending_confirmation").commit())
            }
            installer.abandonSession(id)
            throw e
        } finally { prepared.file.delete() }
    }
}

/** Explicit private receiver: web content and external apps cannot supply installer intents. */
class UpdateResultReceiver: BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        val preferences=context.applicationContext.getSharedPreferences("native-update",Context.MODE_PRIVATE)
        synchronized(UpdateState.lock) {
        val session=intent.getIntExtra(PackageInstaller.EXTRA_SESSION_ID,-1)
        if(session<0 || session!=preferences.getInt("session",-2))return
        val status=intent.getIntExtra(PackageInstaller.EXTRA_STATUS,PackageInstaller.STATUS_FAILURE)
        if(status==PackageInstaller.STATUS_PENDING_USER_ACTION) {
            check(preferences.edit().putBoolean("pending_confirmation",true)
                .putString("result","Waiting for Android installation confirmation. If it was interrupted, return to AgentNet and try Update again.").commit())
            @Suppress("DEPRECATION") val confirmation=intent.getParcelableExtra<Intent>(Intent.EXTRA_INTENT)
            if(confirmation!=null)runCatching { context.startActivity(confirmation.addFlags(Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_MULTIPLE_TASK)) }.onFailure {
                context.packageManager.packageInstaller.abandonSession(session)
                preferences.edit().remove("session").remove("pending_confirmation").putString("result","Android could not open installation confirmation. Try again.").apply()
            }
            return
        }
        val result=when(status) {
            PackageInstaller.STATUS_SUCCESS -> "AgentNet was updated."
            PackageInstaller.STATUS_FAILURE_ABORTED -> "Installation was canceled. Your messages and identity were kept. Try again when ready."
            else -> "Android did not install the update. Your data was kept. Check storage and installation permission, then try again."
        }
        preferences.edit().remove("session").remove("pending_confirmation").putString("result",result).commit()
        }
    }
}
