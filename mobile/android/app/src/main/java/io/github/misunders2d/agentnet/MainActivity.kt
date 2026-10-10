package io.github.misunders2d.agentnet

import android.Manifest
import android.app.Activity
import android.app.AlertDialog
import android.content.Intent
import android.content.pm.PackageManager
import android.graphics.Color
import android.net.Uri
import android.os.Build
import android.os.Bundle
import android.util.Base64
import android.view.View
import android.view.WindowInsets
import android.webkit.CookieManager
import android.webkit.SslErrorHandler
import android.net.http.SslError
import android.webkit.ValueCallback
import android.webkit.WebChromeClient
import android.webkit.WebResourceRequest
import android.webkit.WebResourceResponse
import android.webkit.WebSettings
import android.webkit.WebView
import android.webkit.WebViewClient
import android.widget.Button
import android.widget.LinearLayout
import android.widget.TextView
import androidx.webkit.JavaScriptReplyProxy
import androidx.webkit.WebMessageCompat
import androidx.webkit.WebViewCompat
import androidx.webkit.WebViewFeature
import org.json.JSONObject
import java.io.ByteArrayInputStream
import java.io.File
import java.util.concurrent.Executors

/** Presentation-only shell around the exact Comic host served by the embedded core. */
class MainActivity : Activity() {
    private val host get() = (application as AgentNetApp).host
    private var web: WebView? = null
    private var policy: WebPolicy? = null
    private var loaded = false
    private var resumed = false
    private var destroyed = false
    private var backPending = false
    private val files = Executors.newSingleThreadExecutor()
    private lateinit var exports: ExportTransfer
    private var chosenFiles: ValueCallback<Array<Uri>>? = null
    private var permissionReply: ((Boolean) -> Unit)? = null
    private var saving: ExportTransfer.Ready? = null
    private var saveReply: ((Boolean, String?) -> Unit)? = null
    private var destination = ""
    private val observer: (HostState) -> Unit = { showHost(it) }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        if (Build.VERSION.SDK_INT >= 30) window.setDecorFitsSystemWindows(false)
        if (Build.VERSION.SDK_INT >= 33) onBackInvokedDispatcher.registerOnBackInvokedCallback(
            android.window.OnBackInvokedDispatcher.PRIORITY_DEFAULT) { handleBack() }
        exports = ExportTransfer(File(cacheDir, "attachment-exports"))
        // Stale plaintext from interrupted exports is never a retained attachment.
        File(cacheDir, "attachment-exports").listFiles()?.forEach { it.delete() }
        WebView.setWebContentsDebuggingEnabled(BuildConfig.DEBUG)
        destination = WebPolicy.notificationDestination(intent.getStringExtra("workspace").orEmpty(), intent.getStringExtra("destination").orEmpty()).orEmpty()
        showOpening()
        host.observe(observer)
    }
    override fun onStart() {
        super.onStart(); host.foreground(true)
        if (host.keepConnected()) startForegroundService(Intent(this, ConnectionService::class.java))
    }
    override fun onResume() {
        super.onResume(); resumed = true; showHost(host.state)
        web?.visibility = View.VISIBLE; web?.onResume(); applyVisibility()
    }
    override fun onPause() { resumed = false; applyVisibility(); web?.onPause(); super.onPause() }
    override fun onStop() { resumed = false; applyVisibility(); web?.visibility = View.INVISIBLE; host.foreground(false); super.onStop() }
    override fun onDestroy() {
        destroyed = true; host.remove(observer)
        chosenFiles?.onReceiveValue(null); chosenFiles = null
        files.execute { exports.close() }; files.shutdown()
        web?.apply { stopLoading(); webChromeClient = null; removeAllViews(); destroy() }; web = null
        super.onDestroy()
    }
    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent); setIntent(intent)
        destination = WebPolicy.notificationDestination(intent.getStringExtra("workspace").orEmpty(), intent.getStringExtra("destination").orEmpty()).orEmpty(); routeNotification()
    }
    private fun showOpening(error: String = "") {
        val panel = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL; setPadding(32, 48, 32, 32); setBackgroundColor(Color.rgb(242, 239, 255))
            addView(TextView(this@MainActivity).apply { text = error.ifEmpty { "Opening AgentNet…" }; textSize = 18f })
            if (error.isNotEmpty()) addView(Button(this@MainActivity).apply { text = "Try again"; setOnClickListener { host.open() } })
        }
        setContentView(panel)
    }
    private fun showHost(state: HostState) {
        if (destroyed || loaded || !resumed) return
        if (state.error.isNotEmpty()) { showOpening(state.error); return }
        if (state.url.isEmpty()) return
        if (!WebViewFeature.isFeatureSupported(WebViewFeature.WEB_MESSAGE_LISTENER)) {
            showOpening("Update Android System WebView to open AgentNet securely."); return
        }
        val guard = WebPolicy(state.origin); policy = guard
        val page = WebView(this); web = page
        page.setBackgroundColor(Color.rgb(242, 239, 255))
        page.settings.apply {
            javaScriptEnabled = true; domStorageEnabled = true
            allowFileAccess = false; allowContentAccess = false
            @Suppress("DEPRECATION")
            allowFileAccessFromFileURLs = false
            @Suppress("DEPRECATION")
            allowUniversalAccessFromFileURLs = false
            mixedContentMode = WebSettings.MIXED_CONTENT_NEVER_ALLOW
            javaScriptCanOpenWindowsAutomatically = false; setSupportMultipleWindows(false)
            setGeolocationEnabled(false); mediaPlaybackRequiresUserGesture = true
            safeBrowsingEnabled = true
        }
        CookieManager.getInstance().setAcceptCookie(true)
        CookieManager.getInstance().setAcceptThirdPartyCookies(page, false)
        page.webViewClient = object : WebViewClient() {
            override fun shouldOverrideUrlLoading(view: WebView, request: WebResourceRequest): Boolean {
                val url = request.url.toString()
                if (guard.internal(url)) return false
                if (request.isForMainFrame && request.hasGesture() && guard.external(url)) {
                    runCatching { startActivity(Intent(Intent.ACTION_VIEW, request.url).addCategory(Intent.CATEGORY_BROWSABLE)) }
                }
                return true
            }
            override fun shouldInterceptRequest(view: WebView, request: WebResourceRequest): WebResourceResponse? {
                if (guard.internal(request.url.toString())) return null
                return WebResourceResponse("text/plain", "UTF-8", 403, "Blocked", emptyMap(), ByteArrayInputStream(ByteArray(0)))
            }
            override fun onReceivedSslError(view: WebView, handler: SslErrorHandler, error: SslError) { handler.cancel() }
            override fun onPageFinished(view: WebView, url: String) { if (guard.internal(url)) { applyVisibility(); routeNotification() } }
        }
        page.webChromeClient = object : WebChromeClient() {
            override fun onShowFileChooser(view: WebView, callback: ValueCallback<Array<Uri>>, params: FileChooserParams): Boolean {
                if (!guard.internal(view.url.orEmpty()) || chosenFiles != null) return false
                chosenFiles = callback
                val types = params.acceptTypes.filter { it.matches(Regex("[A-Za-z0-9.+-]+/[A-Za-z0-9.*+-]+")) }.distinct()
                val choose = Intent(Intent.ACTION_OPEN_DOCUMENT).addCategory(Intent.CATEGORY_OPENABLE).setType(types.singleOrNull() ?: "*/*")
                    .putExtra(Intent.EXTRA_ALLOW_MULTIPLE, params.mode == FileChooserParams.MODE_OPEN_MULTIPLE)
                if (types.size > 1) choose.putExtra(Intent.EXTRA_MIME_TYPES, types.toTypedArray())
                try { startActivityForResult(choose, PICK_FILES) }
                catch (_: Exception) { chosenFiles = null; callback.onReceiveValue(null) }
                return true
            }
            override fun onPermissionRequest(request: android.webkit.PermissionRequest) { request.deny() }
        }
        WebViewCompat.addWebMessageListener(page, "AgentNetAndroid", setOf(state.origin)) { _, message, source, mainFrame, reply ->
            if (!destroyed && guard.bridge(source.toString(), mainFrame) && message.type == WebMessageCompat.TYPE_STRING) {
                bridge(message.data.orEmpty(), reply)
            }
        }
        val frame = android.widget.FrameLayout(this).apply { setBackgroundColor(Color.rgb(79, 51, 196)); addView(page) }
        frame.setOnApplyWindowInsetsListener { v, insets ->
            if (Build.VERSION.SDK_INT >= 30) {
                val bars = insets.getInsets(WindowInsets.Type.systemBars() or WindowInsets.Type.displayCutout())
                val ime = insets.getInsets(WindowInsets.Type.ime())
                v.setPadding(bars.left, bars.top, bars.right, maxOf(bars.bottom, ime.bottom))
            } else {
                @Suppress("DEPRECATION")
                v.setPadding(insets.systemWindowInsetLeft, insets.systemWindowInsetTop, insets.systemWindowInsetRight, insets.systemWindowInsetBottom)
            }
            insets
        }
        setContentView(frame); frame.requestApplyInsets()
        loaded = true; page.loadUrl(state.url) // authenticated URL stays in memory, never logs
    }
    private fun applyVisibility() {
        val page = web ?: return
        if (policy?.internal(page.url.orEmpty()) == true) page.evaluateJavascript(
            "window.agentnetNativeVisibility && window.agentnetNativeVisibility($resumed)", null)
    }
    private fun routeNotification() {
        val fragment = destination
        val guard = policy ?: return
        val page = web ?: return
        if (fragment.isNotEmpty() && guard.internal(page.url.orEmpty())) {
            destination = ""
            page.evaluateJavascript("location.hash=" + JSONObject.quote(fragment), null)
        }
    }
    @Deprecated("Platform callback remains supported for minSdk26")
    override fun onBackPressed() { handleBack() }
    private fun handleBack() {
        if (backPending) return
        val page = web
        if (page == null || policy?.internal(page.url.orEmpty()) != true) { finish(); return }
        backPending = true
        page.evaluateJavascript("Boolean(window.agentnetNativeBack && window.agentnetNativeBack())") { consumed ->
            backPending = false
            if (consumed != "true") finish()
        }
    }
    private fun permitted(): Boolean = Build.VERSION.SDK_INT < 33 || checkSelfPermission(Manifest.permission.POST_NOTIFICATIONS) == PackageManager.PERMISSION_GRANTED
    private fun askPermission(done: (Boolean) -> Unit) {
        if (permitted()) { done(true); return }
        if (permissionReply != null) { done(false); return }
        permissionReply = done; requestPermissions(arrayOf(Manifest.permission.POST_NOTIFICATIONS), NOTIFICATIONS)
    }
    private fun bridge(raw: String, proxy: JavaScriptReplyProxy) {
        if (raw.length > 800 * 1024) return
        val request = runCatching { JSONObject(raw) }.getOrNull() ?: return
        if (request.optString("type") !in setOf("clipboard:write", "drafts:ack") && raw.length > 72 * 1024) return
        val id = request.optString("id"); if (id.isEmpty() || id.length > 128) return
        fun reply(data: JSONObject = JSONObject(), error: String? = null) {
            runOnUiThread {
                if (!destroyed) proxy.postMessage(data.put("id", id).put("ok", error == null).apply { if (error != null) put("error", error) }.toString())
            }
        }
        try {
            when (request.optString("type")) {
                "drafts:legacy" -> files.execute {
                    val drafts = org.json.JSONArray()
                    LegacyDrafts.recover(getSharedPreferences("drafts", MODE_PRIVATE).all).forEach { (key, value) ->
                        drafts.put(JSONObject().put("key", key).put("value", value))
                    }
                    reply(JSONObject().put("drafts", drafts))
                }
                "drafts:ack" -> files.execute {
                    try {
                        val key = request.getString("key"); val value = request.getString("value")
                        val preferences = getSharedPreferences("drafts", MODE_PRIVATE)
                        val matches = LegacyDrafts.acknowledged(key, value, preferences.all[key])
                        val removed = matches && preferences.edit().remove(key).commit()
                        reply(JSONObject().put("removed", removed))
                    } catch (_: Exception) { reply(error = "Invalid draft acknowledgement.") }
                }
                "clipboard:write" -> {
                    require(resumed)
                    val text = request.getString("text")
                    require(text.length <= 128 * 1024)
                    getSystemService(android.content.ClipboardManager::class.java)
                        .setPrimaryClip(android.content.ClipData.newPlainText("AgentNet", text))
                    reply()
                }
                "connection:get" -> reply(JSONObject().put("enabled", host.keepConnected()))
                "connection:set" -> {
                    require(request.get("enabled") is Boolean)
                    val enabled = request.getBoolean("enabled")
                    if (enabled) askPermission { granted ->
                        if (granted) { host.setKeepConnected(true); startForegroundService(Intent(this, ConnectionService::class.java)) }
                        reply(JSONObject().put("enabled", host.keepConnected()))
                    } else {
                        host.setKeepConnected(false); stopService(Intent(this, ConnectionService::class.java)); host.background(false)
                        reply(JSONObject().put("enabled", false))
                    }
                }
                "notifications:status" -> reply(JSONObject().put("granted", permitted()))
                "notifications:request" -> askPermission { reply(JSONObject().put("granted", it)) }
                "export:start" -> files.execute {
                    try {
                        val number = request.get("size") as? Number ?: throw IllegalArgumentException()
                        val size = number.toLong(); require(number.toDouble() == size.toDouble())
                        reply(JSONObject().put("transfer", exports.begin(request.getString("name"), request.optString("mime"), size)))
                    } catch (_: Exception) { reply(error = "Cannot start this download. Check its size or finish the current download.") }
                }
                "export:chunk" -> files.execute {
                    try {
                        val data = request.getString("data"); require(data.length <= 65536)
                        val bytes = Base64.decode(data, Base64.NO_WRAP)
                        reply(JSONObject().put("written", exports.append(request.getString("transfer"), bytes)))
                    } catch (_: Exception) { reply(error = "The download chunk is invalid.") }
                }
                "export:cancel" -> files.execute {
                    try { exports.cancel(request.getString("transfer")); reply() }
                    catch (_: Exception) { reply(error = "Unknown download.") }
                }
                "export:finish" -> files.execute {
                    try {
                        val ready = exports.finish(request.getString("transfer"))
                        runOnUiThread {
                            if (destroyed) { return@runOnUiThread }
                            saving = ready; saveReply = { saved, error -> reply(JSONObject().put("saved", saved), error) }
                            try { startActivityForResult(Intent(Intent.ACTION_CREATE_DOCUMENT).addCategory(Intent.CATEGORY_OPENABLE).setType(ready.mime).putExtra(Intent.EXTRA_TITLE, ready.name), SAVE_FILE) }
                            catch (_: Exception) { saving = null; saveReply = null; files.execute { exports.close() }; reply(error = "No document provider is available to save this file.") }
                        }
                    } catch (_: Exception) { reply(error = "Download incomplete. Try Download again.") }
                }
                else -> reply(error = "Unknown native operation.")
            }
        } catch (_: Exception) { reply(error = "Invalid native request.") }
    }
    @Deprecated("Platform result callback supports minSdk26")
    override fun onActivityResult(requestCode: Int, resultCode: Int, data: Intent?) {
        super.onActivityResult(requestCode, resultCode, data)
        if (requestCode == PICK_FILES) {
            val uris = if (resultCode == RESULT_OK) data?.clipData?.let { c -> (0 until c.itemCount.coerceAtMost(100)).map { c.getItemAt(it).uri }.toTypedArray() } ?: data?.data?.let { arrayOf(it) } else null
            chosenFiles?.onReceiveValue(uris?.filter { it.scheme == "content" }?.toTypedArray()); chosenFiles = null
        }
        if (requestCode == SAVE_FILE) {
            val ready = saving; val callback = saveReply; saving = null; saveReply = null
            val uri = data?.data
            if (resultCode != RESULT_OK || ready == null || uri?.scheme != "content") {
                files.execute { exports.close(); runOnUiThread { callback?.invoke(false, null) } }; return
            }
            files.execute {
                var error: String? = null
                try { contentResolver.openOutputStream(uri, "wt")?.use { out -> ready.file.inputStream().use { it.copyTo(out) } } ?: throw IllegalStateException() }
                catch (_: Exception) { error = "Android could not save this download. Try another destination." }
                finally { exports.close() }
                runOnUiThread { callback?.invoke(error == null, error) }
            }
        }
    }
    override fun onRequestPermissionsResult(requestCode: Int, permissions: Array<out String>, results: IntArray) {
        super.onRequestPermissionsResult(requestCode, permissions, results)
        if (requestCode == NOTIFICATIONS) { val callback = permissionReply; permissionReply = null; callback?.invoke(permitted()) }
    }
    companion object { private const val PICK_FILES = 10; private const val SAVE_FILE = 11; private const val NOTIFICATIONS = 12 }
}
