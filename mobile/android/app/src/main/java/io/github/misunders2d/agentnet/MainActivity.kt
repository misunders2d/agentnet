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
import android.view.ViewGroup
import android.view.WindowInsets
import android.view.WindowInsetsController
import android.view.WindowManager
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
import com.journeyapps.barcodescanner.ScanOptions
import com.journeyapps.barcodescanner.ScanIntentResult
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
    private var contentFrame: android.widget.FrameLayout? = null
    private data class Fullscreen(val view: View, val callback: WebChromeClient.CustomViewCallback,
        val systemUi: Int, val flags: Int, val statusVisible: Boolean, val navigationVisible: Boolean, val barsBehavior: Int)
    private var fullscreen: Fullscreen? = null
    private var hidingFullscreen = false
    private val updater by lazy { NativeUpdater(this) }
    private val updatePreferences by lazy { getSharedPreferences("native-update", MODE_PRIVATE) }
    private val updateResultListener = android.content.SharedPreferences.OnSharedPreferenceChangeListener { _, key ->
        if (key == "result") runOnUiThread { if (resumed && !destroyed) applyVisibility() }
    }
    private val updates = Executors.newSingleThreadExecutor()
    private val updateBusy = java.util.concurrent.atomic.AtomicBoolean(false)
    private var pendingUpdate: NativeUpdater.Prepared? = null
    private var updatePermissionReply: ((Boolean, String?) -> Unit)? = null
    private var scanReply: ((String?, String?) -> Unit)? = null
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
        updatePreferences.registerOnSharedPreferenceChangeListener(updateResultListener)
        if (Build.VERSION.SDK_INT >= 30) window.setDecorFitsSystemWindows(false)
        if (Build.VERSION.SDK_INT >= 33) onBackInvokedDispatcher.registerOnBackInvokedCallback(
            android.window.OnBackInvokedDispatcher.PRIORITY_DEFAULT) { handleBack() }
        exports = ExportTransfer(File(cacheDir, "attachment-exports"))
        // Stale plaintext from interrupted exports is never a retained attachment.
        File(cacheDir, "attachment-exports").listFiles()?.forEach { it.delete() }
        WebView.setWebContentsDebuggingEnabled(BuildConfig.DEBUG)
        destination = intentDestination(intent)
        showOpening()
        host.observe(observer)
    }
    override fun onStart() {
        super.onStart(); host.foreground(true)
        if (host.keepConnected()) startForegroundService(Intent(this, ConnectionService::class.java))
    }
    override fun onResume() {
        super.onResume(); resumed = true; showHost(host.state)
        web?.visibility = View.VISIBLE; web?.onResume(); focusPageIfUnowned(); applyVisibility()
    }
    override fun onWindowFocusChanged(hasFocus: Boolean) {
        super.onWindowFocusChanged(hasFocus)
        if (hasFocus) focusPageIfUnowned()
    }
    override fun onPause() { resumed = false; hideCustomView(); applyVisibility(); web?.onPause(); super.onPause() }
    override fun onStop() { resumed = false; applyVisibility(); web?.visibility = View.INVISIBLE; host.foreground(false); super.onStop() }
    override fun onDestroy() {
        destroyed = true; hideCustomView(); updatePreferences.unregisterOnSharedPreferenceChangeListener(updateResultListener); host.remove(observer)
        chosenFiles?.onReceiveValue(null); chosenFiles = null
        scanReply = null
        pendingUpdate?.file?.delete(); pendingUpdate = null; updatePermissionReply = null
        updates.shutdown()
        files.execute { exports.close() }; files.shutdown()
        web?.apply { stopLoading(); webChromeClient = null; removeAllViews(); destroy() }; web = null
        super.onDestroy()
    }
    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent); setIntent(intent)
        destination = intentDestination(intent); routeNotification()
    }
    private fun intentDestination(intent: Intent): String =
        if(intent.action == Intent.ACTION_VIEW)WebPolicy.externalLink(intent.dataString.orEmpty()).orEmpty()
        else WebPolicy.notificationDestination(intent.getStringExtra("workspace").orEmpty(),intent.getStringExtra("destination").orEmpty()).orEmpty()
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
        page.isFocusableInTouchMode = true
        page.setBackgroundColor(Color.rgb(242, 239, 255))
        page.settings.apply {
            // Native document focus must not choose an HTML node or move its caret.
            setNeedInitialFocus(false)
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
                val url = request.url.toString()
                if (guard.internal(url) || guard.gallery(url, request.method, request.isForMainFrame)) return null
                return WebResourceResponse("text/plain", "UTF-8", 403, "Blocked", emptyMap(), ByteArrayInputStream(ByteArray(0)))
            }
            override fun onReceivedSslError(view: WebView, handler: SslErrorHandler, error: SslError) { handler.cancel() }
            override fun onPageFinished(view: WebView, url: String) {
                if (guard.internal(url)) {
                    // The Activity may gain focus before the asynchronous host
                    // creates/attaches its WebView. Recheck at document readiness;
                    // the existing ownership guard preserves editors and dialogs.
                    focusPageIfUnowned(); applyVisibility(); routeNotification()
                }
            }
        }
        page.webChromeClient = object : WebChromeClient() {
            override fun onShowCustomView(view: View, callback: CustomViewCallback) {
                if (!guard.internal(page.url.orEmpty())) { callback.onCustomViewHidden(); return }
                showCustomView(view, callback)
            }
            override fun onHideCustomView() { hideCustomView() }
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
        contentFrame = frame
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
        focusPageIfUnowned()
    }
    private fun focusPageIfUnowned() {
        val page = web ?: return
        if (destroyed || !resumed || fullscreen != null || !page.isAttachedToWindow ||
            !page.isShown || !page.hasWindowFocus() || page.hasFocus()) return
        val owner = window.decorView.findFocus()
        // Give the visible document initial native focus without replacing an
        // existing editor, custom view, dialog or other native focus owner.
        if (owner == null || owner === window.decorView || owner === contentFrame) page.requestFocus()
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
        if (fullscreen != null) { hideCustomView(); return }
        if (backPending) return
        val page = web
        if (page == null || policy?.internal(page.url.orEmpty()) != true) { finish(); return }
        backPending = true
        page.evaluateJavascript("Boolean(window.agentnetNativeBack && window.agentnetNativeBack())") { consumed ->
            backPending = false
            if (consumed != "true") finish()
        }
    }
    @Suppress("DEPRECATION")
    private fun showCustomView(view: View, callback: WebChromeClient.CustomViewCallback) {
        if (destroyed || !resumed || fullscreen != null || hidingFullscreen || view.parent != null) {
            callback.onCustomViewHidden(); return
        }
        val decor = window.decorView as ViewGroup
        val insets = if (Build.VERSION.SDK_INT >= 30) decor.rootWindowInsets else null
        val saved = Fullscreen(view, callback, decor.systemUiVisibility, window.attributes.flags,
            if (Build.VERSION.SDK_INT >= 30) insets?.isVisible(WindowInsets.Type.statusBars()) ?: true else true,
            if (Build.VERSION.SDK_INT >= 30) insets?.isVisible(WindowInsets.Type.navigationBars()) ?: true else true,
            if (Build.VERSION.SDK_INT >= 30) window.insetsController?.systemBarsBehavior ?: 0 else 0)
        fullscreen = saved
        decor.addView(view, ViewGroup.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.MATCH_PARENT))
        if (Build.VERSION.SDK_INT >= 30) {
            window.insetsController?.apply {
                systemBarsBehavior = WindowInsetsController.BEHAVIOR_SHOW_TRANSIENT_BARS_BY_SWIPE
                hide(WindowInsets.Type.systemBars())
            }
        } else {
            window.setFlags(WindowManager.LayoutParams.FLAG_FULLSCREEN,
                WindowManager.LayoutParams.FLAG_FULLSCREEN or WindowManager.LayoutParams.FLAG_FORCE_NOT_FULLSCREEN)
            decor.systemUiVisibility = saved.systemUi or View.SYSTEM_UI_FLAG_FULLSCREEN or View.SYSTEM_UI_FLAG_HIDE_NAVIGATION or
                View.SYSTEM_UI_FLAG_IMMERSIVE_STICKY or View.SYSTEM_UI_FLAG_LAYOUT_STABLE or
                View.SYSTEM_UI_FLAG_LAYOUT_HIDE_NAVIGATION or View.SYSTEM_UI_FLAG_LAYOUT_FULLSCREEN
        }
    }
    @Suppress("DEPRECATION")
    private fun hideCustomView() {
        val saved = fullscreen ?: return
        fullscreen = null // clear before the foreign callback can reenter
        hidingFullscreen = true
        try {
            (saved.view.parent as? ViewGroup)?.removeView(saved.view)
            window.setFlags(saved.flags,
                WindowManager.LayoutParams.FLAG_FULLSCREEN or WindowManager.LayoutParams.FLAG_FORCE_NOT_FULLSCREEN)
            window.decorView.systemUiVisibility = saved.systemUi
            if (Build.VERSION.SDK_INT >= 30) {
                window.insetsController?.apply {
                    systemBarsBehavior = saved.barsBehavior
                    if (saved.statusVisible) show(WindowInsets.Type.statusBars()) else hide(WindowInsets.Type.statusBars())
                    if (saved.navigationVisible) show(WindowInsets.Type.navigationBars()) else hide(WindowInsets.Type.navigationBars())
                }
            }
            contentFrame?.requestApplyInsets()
            saved.callback.onCustomViewHidden()
        } finally { hidingFullscreen = false }
    }
    private fun permitted(): Boolean = host.notificationsPermitted()
    private fun askPermission(done: (Boolean) -> Unit) {
        if (permitted()) { done(true); return }
        if (Build.VERSION.SDK_INT < 33 || checkSelfPermission(Manifest.permission.POST_NOTIFICATIONS) == PackageManager.PERMISSION_GRANTED) {
            // The app or activity channel was disabled in Android settings.
            // Runtime permission cannot re-enable it, and we never change it.
            done(false); return
        }
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
                "app:status", "app:check", "app:update" -> {
                    require(BuildConfig.SELF_UPDATE) { "This installation is updated through its app store." }
                    require(request.keys().asSequence().all { it == "id" || it == "type" }) { "Update requests cannot select an installation source." }
                    val action = request.getString("type")
                    if (action == "app:status") { reply(JSONObject().put("result", updater.status())); return }
                    require(resumed && updateBusy.compareAndSet(false, true)) { "An update operation is already active or the app is not visible." }
                    updates.execute {
                        try {
                            if (action == "app:check") {
                                reply(JSONObject().put("result", updater.check())); updateBusy.set(false)
                            } else {
                                val prepared = updater.prepare()
                                runOnUiThread {
                                    installPreparedUpdate(prepared) { result, error ->
                                        reply(JSONObject().apply { if (result != null) put("result", result) }, error)
                                    }
                                }
                            }
                        } catch (e: Exception) { updateBusy.set(false); reply(error = e.message ?: "Could not check the official release. Try again.") }
                    }
                }
                "qr:scan" -> {
                    require(resumed && scanReply == null)
                    if (!packageManager.hasSystemFeature(PackageManager.FEATURE_CAMERA_ANY)) {
                        reply(error = "This device has no camera. Paste the invitation link instead.")
                        return
                    }
                    scanReply = { text, error ->
                        reply(JSONObject().apply { if (text == null) put("canceled", true) else put("text", text) }, error)
                    }
                    try {
                        val scan = ScanOptions().setDesiredBarcodeFormats(ScanOptions.QR_CODE)
                            .setPrompt("Scan an AgentNet invitation or device link")
                            .setBeepEnabled(false).setBarcodeImageEnabled(false).setOrientationLocked(false)
                            .createScanIntent(this)
                        startActivityForResult(scan, SCAN_QR)
                    } catch (_: Exception) {
                        val callback = scanReply; scanReply = null
                        callback?.invoke(null, "The camera scanner could not open. Paste the invitation link instead.")
                    }
                }
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
                "connection:get" -> {
                    // Upgraded installs retain their registered channel's choices.
                    // A newly constructed channel does not report those choices.
                    val channel = getSystemService(android.app.NotificationManager::class.java).getNotificationChannel(CONNECTION_CHANNEL)
                    reply(JSONObject().put("enabled", host.keepConnected()).put("badging", channel?.canShowBadge() == true))
                }
                "connection:settings" -> {
                    require(resumed && request.keys().asSequence().all { it == "id" || it == "type" })
                    startActivity(connectionNotificationSettings(packageName))
                    reply(JSONObject().put("opened", true))
                }
                "connection:set" -> {
                    require(request.get("enabled") is Boolean)
                    val enabled = request.getBoolean("enabled")
                    if (enabled) askPermission { granted ->
                        try {
                            if (granted) {
                                host.setKeepConnected(true)
                                try { startForegroundService(Intent(this, ConnectionService::class.java)) }
                                catch (e: Exception) { host.setKeepConnected(false); host.background(false); throw e }
                            }
                            reply(JSONObject().put("enabled", host.keepConnected()))
                        } catch (_: Exception) { reply(error = "Android could not start the background connection. Try again from Connection settings.") }
                    } else {
                        host.setKeepConnected(false); stopService(Intent(this, ConnectionService::class.java)); host.background(false)
                        reply(JSONObject().put("enabled", false))
                    }
                }
                "notifications:status" -> reply(host.notificationStatus())
                "notifications:settings" -> {
                    require(resumed && request.keys().asSequence().all { it == "id" || it == "type" })
                    host.prepareNotificationChannel()
                    startActivity(activityNotificationSettings(packageName))
                    reply(JSONObject().put("opened", true))
                }
                "notifications:request" -> askPermission { granted ->
                    try {
                        if (granted) host.prepareNotificationChannel()
                        host.afterNotificationConsent(granted) { startForegroundService(Intent(this, ConnectionService::class.java)) }
                        reply(host.notificationStatus())
                    } catch (_: Exception) { reply(error = "Android allowed notifications, but could not start the background connection. Try again from Connection settings.") }
                }
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
    /** Used only after the fixed-origin download passed checksum verification. */
    private fun installPreparedUpdate(prepared: NativeUpdater.Prepared, done: (JSONObject?, String?) -> Unit) {
        if (!BuildConfig.SELF_UPDATE || destroyed || !resumed) {
            prepared.file.delete(); updateBusy.set(false); done(null, "Return to AgentNet and try the update again."); return
        }
        updateBusy.set(true)
        fun install() {
            updates.execute {
                try {
                    updater.install(prepared)
                    done(JSONObject().put("state", "installer").put("message", "Confirm the update in Android’s package installer. Your messages and identity are kept."), null)
                } catch (e: Exception) {
                    prepared.file.delete(); done(null, e.message ?: "Android could not start the update. Try again.")
                } finally { updateBusy.set(false) }
            }
        }
        if (packageManager.canRequestPackageInstalls()) install()
        else {
            pendingUpdate = prepared
            updatePermissionReply = { granted, error ->
                pendingUpdate = null
                if (granted) install()
                else {
                    prepared.file.delete(); updateBusy.set(false)
                    done(null, error ?: "Installation permission was not granted. Your data was kept; you can try again.")
                }
            }
            try { startActivityForResult(Intent(android.provider.Settings.ACTION_MANAGE_UNKNOWN_APP_SOURCES, Uri.parse("package:$packageName")), INSTALL_PERMISSION) }
            catch (_: Exception) {
                val callback = updatePermissionReply; updatePermissionReply = null
                callback?.invoke(false, "Android could not open installation permission settings.")
            }
        }
    }
    @Deprecated("Platform result callback supports minSdk26")
    override fun onActivityResult(requestCode: Int, resultCode: Int, data: Intent?) {
        super.onActivityResult(requestCode, resultCode, data)
        if (requestCode == INSTALL_PERMISSION) {
            val callback = updatePermissionReply; updatePermissionReply = null
            callback?.invoke(packageManager.canRequestPackageInstalls(), null)
            return
        }
        if (requestCode == SCAN_QR) {
            val callback = scanReply; scanReply = null
            if (resultCode != RESULT_OK) {
                val denied = data?.getBooleanExtra("MISSING_CAMERA_PERMISSION", false) == true
                callback?.invoke(null, if (denied) "Camera permission was declined. Paste the invitation link instead." else null)
            } else {
                try {
                    val text = ScanIntentResult.parseActivityResult(resultCode, data)?.contents
                        ?: throw IllegalArgumentException()
                    callback?.invoke(WebPolicy.scanText(text), null)
                } catch (_: Exception) { callback?.invoke(null, "This QR code could not be read. Paste the complete invitation link instead.") }
            }
            return
        }
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
    companion object { private const val PICK_FILES = 10; private const val SAVE_FILE = 11; private const val NOTIFICATIONS = 12; private const val SCAN_QR = 13; private const val INSTALL_PERMISSION = 14 }
}
