package io.github.misunders2d.agentnet

import android.Manifest
import android.app.Activity
import android.app.AlertDialog
import android.content.Intent
import android.content.pm.PackageManager
import android.graphics.Color
import android.graphics.Typeface
import android.graphics.drawable.GradientDrawable
import android.os.Build
import android.os.Bundle
import android.text.Editable
import android.text.TextWatcher
import android.text.InputType
import android.text.TextUtils
import android.view.Gravity
import android.view.View
import android.view.ViewGroup
import android.view.WindowInsets
import android.view.inputmethod.EditorInfo
import android.widget.*
import java.time.OffsetDateTime
import java.time.format.DateTimeFormatter

class MainActivity : Activity() {
    private val repo get() = (application as AgentNetApp).repository
    private val cream = Color.rgb(255, 249, 237)
    private val ink = Color.rgb(33, 24, 50)
    private val muted = Color.rgb(99, 87, 112)
    private val yellow = Color.rgb(255, 216, 66)
    private var page = ""
    private lateinit var root: LinearLayout
    private lateinit var status: TextView
    private lateinit var errors: TextView
    private var list: ListView? = null
    private var composer: EditText? = null
    private var send: Button? = null
    private var kind: Spinner? = null
    private var chatRows = emptyList<Chat>()
    private var messageRows = emptyList<ChatMessage>()
    private var linkButton: Button? = null
    private var discardButton: Button? = null
    private var showing = false
    private val observer: (ScreenState) -> Unit = { render(it) }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        root = LinearLayout(this).apply { orientation = LinearLayout.VERTICAL; setBackgroundColor(cream) }
        setContentView(root)
        root.setOnApplyWindowInsetsListener { view, insets ->
            if (Build.VERSION.SDK_INT >= 30) {
                val i = insets.getInsets(WindowInsets.Type.systemBars() or WindowInsets.Type.ime())
                view.setPadding(i.left, i.top, i.right, i.bottom)
            } else {
                @Suppress("DEPRECATION")
                view.setPadding(insets.systemWindowInsetLeft, insets.systemWindowInsetTop,
                    insets.systemWindowInsetRight, insets.systemWindowInsetBottom)
            }
            insets
        }
        repo.observe(observer)
    }

    override fun onStart() {
        super.onStart(); showing = true; repo.foreground(true)
        if (repo.keepConnected()) startForegroundService(Intent(this, ConnectionService::class.java))
    }
    override fun onStop() { showing = false; repo.foreground(false); super.onStop() }
    override fun onDestroy() { repo.remove(observer); super.onDestroy() }
    @Deprecated("Platform callback remains supported for minSdk26")
    override fun onBackPressed() { if (repo.state.selected != null) repo.select(null) else super.onBackPressed() }

    private fun dp(value: Int) = (value * resources.displayMetrics.density).toInt()
    private fun text(value: String, size: Float = 16f, bold: Boolean = false) = TextView(this).apply {
        text = value; textSize = size; setTextColor(ink)
        if (bold) setTypeface(typeface, Typeface.BOLD)
        setPadding(dp(16), dp(8), dp(16), dp(8))
    }
    private fun button(value: String, action: () -> Unit) = Button(this).apply {
        text = value; isAllCaps = false; setTextColor(ink); minHeight = dp(48)
        setOnClickListener { action() }
    }
    private fun reset(title: String, back: Boolean = false) {
        root.removeAllViews(); list = null; composer = null; send = null; kind = null; linkButton = null; discardButton = null
        val header = LinearLayout(this).apply { gravity = Gravity.CENTER_VERTICAL }
        if (back) header.addView(button("‹ Chats") { repo.select(null) })
        header.addView(text(title, 24f, true).apply {
            maxLines = 2; ellipsize = TextUtils.TruncateAt.END
        }, LinearLayout.LayoutParams(0, -2, 1f))
        header.addView(button("⋮") { connectionOptions() }.apply { contentDescription = "Connection options" }, LinearLayout.LayoutParams(dp(56), -2))
        root.addView(header)
        status = text("", 13f).apply { setTextColor(muted) }; root.addView(status)
        errors = text("", 14f).apply { setTextColor(Color.rgb(150, 30, 40)); accessibilityLiveRegion = View.ACCESSIBILITY_LIVE_REGION_POLITE }; root.addView(errors)
    }

    private fun render(s: ScreenState) {
        val next = if (!s.linked) "setup" else s.selected?.key ?: "chats"
        if (next != page) {
            page = next
            when { next == "setup" -> setup(); s.selected == null -> chats(); else -> conversation(s.selected) }
        }
        status.text = s.connection
        errors.text = s.error.ifBlank { s.frozen }.ifBlank { s.transportError }
        errors.visibility = if (errors.text.isBlank()) View.GONE else View.VISIBLE
        linkButton?.isEnabled = !s.busy
        linkButton?.text = if (s.busy) "Linking…" else "Link this phone"
        if (s.linked && s.selected == null) {
            chatRows = s.chats; (list?.adapter as? BaseAdapter)?.notifyDataSetChanged()
        } else if (s.selected != null) {
            val changed = messageRows != s.messages
            val atBottom = messageRows.isEmpty() || (list?.lastVisiblePosition ?: 0) >= messageRows.lastIndex - 1
            if (changed) {
                messageRows = s.messages; (list?.adapter as? BaseAdapter)?.notifyDataSetChanged()
                if (atBottom) list?.post { list?.setSelection(messageRows.lastIndex.coerceAtLeast(0)); markVisible() }
            }
            val attempted = repo.draftFrozen(s.selected)
            composer?.isEnabled = !s.sending && !attempted && s.frozen.isBlank()
            kind?.isEnabled = !s.sending && !attempted
            send?.isEnabled = !s.sending && s.frozen.isBlank()
            send?.text = if (s.sending) "Saving…" else if (attempted) "Retry" else "Send"
            discardButton?.visibility = if (attempted && !s.sending) View.VISIBLE else View.GONE
            if (!s.sending && repo.draft(s.selected).isEmpty() && composer?.text?.isNotEmpty() == true) composer?.setText("")
        }
    }

    private fun setup() {
        reset("AgentNet")
        root.addView(text("Your chats, on this phone", 26f, true))
        root.addView(text("On a device you already use, open Your devices → Add a device. Paste its link code here, then approve this phone there."))
        val name = EditText(this).apply { hint = "Device name"; setText("android-native"); isSingleLine = true; setPadding(dp(16), dp(12), dp(16), dp(12)) }
        val code = EditText(this).apply {
            hint = "Device link code"; minLines = 2; maxLines = 4
            inputType = InputType.TYPE_CLASS_TEXT or InputType.TYPE_TEXT_VARIATION_VISIBLE_PASSWORD or InputType.TYPE_TEXT_FLAG_NO_SUGGESTIONS
            imeOptions = imeOptions or EditorInfo.IME_FLAG_NO_PERSONALIZED_LEARNING
            setPadding(dp(16), dp(12), dp(16), dp(12)); importantForAutofill = View.IMPORTANT_FOR_AUTOFILL_NO
        }
        root.addView(name); root.addView(code)
        val link = button("Link this phone") {
            if (!repo.state.busy && code.text.isNotBlank() && name.text.isNotBlank()) {
                repo.link(code.text.toString(), name.text.toString())
            }
        }
        linkButton = link; root.addView(link)
    }

    private fun chats() {
        reset("Chats")
        val empty = text("No saved chats yet. Once this phone is approved, your other devices can share their history.")
        val rows = ListView(this).apply { dividerHeight = dp(1); setBackgroundColor(cream) }
        list = rows; root.addView(rows, LinearLayout.LayoutParams(-1, 0, 1f)); root.addView(empty)
        rows.emptyView = empty
        rows.adapter = object : BaseAdapter() {
            override fun getCount() = chatRows.size
            override fun getItem(position: Int) = chatRows[position]
            override fun getItemId(position: Int) = position.toLong()
            override fun getView(position: Int, convertView: View?, parent: ViewGroup?): View {
                val chat = chatRows[position]
                return LinearLayout(this@MainActivity).apply {
                    orientation = LinearLayout.VERTICAL; minimumHeight = dp(88)
                    addView(text(chat.title + if (chat.unread > 0) " · ${chat.unread} unread" else "", 18f, true))
                    addView(text(chat.preview.ifBlank { "No messages yet" }, 15f).apply { maxLines = 2; setTextColor(muted) })
                }
            }
        }
        rows.setOnItemClickListener { _, _, position, _ -> repo.select(chatRows[position]) }
    }

    private fun conversation(chat: Chat) {
        reset(chat.title, true); messageRows = emptyList()
        val sendKinds = if (chat.agentId.isNotBlank()) listOf("question", "task") else listOf("message", "question", "task")
        val rows = ListView(this).apply { divider = null; transcriptMode = ListView.TRANSCRIPT_MODE_NORMAL; setStackFromBottom(true) }
        list = rows; root.addView(rows, LinearLayout.LayoutParams(-1, 0, 1f))
        rows.adapter = object : BaseAdapter() {
            override fun getCount() = messageRows.size
            override fun getItem(position: Int) = messageRows[position]
            override fun getItemId(position: Int) = position.toLong()
            override fun getView(position: Int, convertView: View?, parent: ViewGroup?): View {
                val m = messageRows[position]
                return LinearLayout(this@MainActivity).apply {
                    orientation = LinearLayout.VERTICAL; setPadding(dp(if (m.outgoing) 40 else 8), dp(5), dp(if (m.outgoing) 8 else 40), dp(5))
                    val bubble = LinearLayout(this@MainActivity).apply {
                        orientation = LinearLayout.VERTICAL
                        background = GradientDrawable().apply {
                            setColor(if (m.outgoing) Color.rgb(255, 241, 174) else Color.rgb(239, 232, 255))
                            cornerRadius = dp(18).toFloat(); setStroke(dp(1), ink)
                        }
                    }
                    bubble.addView(text(m.author, 12f, true))
                    bubble.addView(text(m.body, 17f).apply { setTextIsSelectable(true) })
                    val time = runCatching { OffsetDateTime.parse(m.at).format(DateTimeFormatter.ofPattern("HH:mm")) }.getOrDefault("")
                    bubble.addView(text(listOf(time, m.state).filter { it.isNotBlank() }.joinToString(" · "), 12f).apply { setTextColor(muted) })
                    addView(bubble)
                }
            }
        }
        rows.setOnScrollListener(object : AbsListView.OnScrollListener {
            override fun onScrollStateChanged(view: AbsListView?, scrollState: Int) { if (scrollState == 0) markVisible() }
            override fun onScroll(view: AbsListView?, first: Int, visible: Int, total: Int) {}
        })
        if (!chat.conversation) {
            val labels = sendKinds.map { when (it) { "question" -> "Ask agent"; "task" -> "Task for agent"; else -> "Message" } }
            kind = Spinner(this).apply { adapter = ArrayAdapter(this@MainActivity, android.R.layout.simple_spinner_dropdown_item, labels) }
            kind?.setSelection(sendKinds.indexOf(repo.draftKind(chat)).coerceAtLeast(0))
            root.addView(kind)
        }
        val compose = LinearLayout(this).apply { gravity = Gravity.BOTTOM; setPadding(dp(8), dp(4), dp(8), dp(8)) }
        composer = EditText(this).apply {
            hint = "Message"; minLines = 1; maxLines = 5; setText(repo.draft(chat)); setTextColor(ink)
            addTextChangedListener(object : TextWatcher {
                override fun beforeTextChanged(s: CharSequence?, start: Int, count: Int, after: Int) {}
                override fun onTextChanged(s: CharSequence?, start: Int, before: Int, count: Int) { repo.saveDraft(chat, s.toString()) }
                override fun afterTextChanged(s: Editable?) {}
            })
        }
        compose.addView(composer, LinearLayout.LayoutParams(0, -2, 1f))
        send = button("Send") { repo.send(chat, composer?.text.toString(), sendKinds[kind?.selectedItemPosition ?: 0]) }.apply { setBackgroundColor(yellow) }
        compose.addView(send); root.addView(compose)
        val discard = button("Discard pending draft") {
            AlertDialog.Builder(this).setTitle("Discard this draft?")
                .setMessage("This removes the saved draft from this phone. A message already queued or delivered stays in the conversation.")
                .setPositiveButton("Discard draft") { _, _ -> repo.discardDraft(chat) }.setNegativeButton("Keep draft", null).show()
        }
        discard.contentDescription = "Discard saved draft without cancelling any message already sent"
        discardButton = discard; root.addView(discard)
    }

    private fun markVisible() {
        if (!showing) return
        val view = list ?: return
        if (repo.state.selected == null) return
        val first = view.firstVisiblePosition.coerceAtLeast(0)
        val last = view.lastVisiblePosition.coerceAtMost(messageRows.lastIndex)
        if (last >= first) repo.markVisibleRead(messageRows.subList(first, last + 1).filter { it.unread }.map { it.id })
    }

    private fun connectionOptions() {
        val enabled = repo.keepConnected()
        AlertDialog.Builder(this).setTitle("Connection")
            .setMessage("Keep connected while the app is in the background. Android shows an ongoing notification. You can stop it there at any time.")
            .setPositiveButton(if (enabled) "Stop background connection" else "Keep connected") { _, _ ->
                if (enabled) {
                    repo.setKeepConnected(false); stopService(Intent(this, ConnectionService::class.java)); repo.background(false)
                } else if (Build.VERSION.SDK_INT >= 33 && checkSelfPermission(Manifest.permission.POST_NOTIFICATIONS) != PackageManager.PERMISSION_GRANTED) {
                    requestPermissions(arrayOf(Manifest.permission.POST_NOTIFICATIONS), 40)
                } else enableBackground()
            }.setNegativeButton("Cancel", null).show()
    }
    private fun enableBackground() { repo.setKeepConnected(true); startForegroundService(Intent(this, ConnectionService::class.java)) }
    override fun onRequestPermissionsResult(requestCode: Int, permissions: Array<out String>, results: IntArray) {
        super.onRequestPermissionsResult(requestCode, permissions, results)
        if (requestCode == 40 && results.firstOrNull() == PackageManager.PERMISSION_GRANTED) enableBackground()
    }
}
