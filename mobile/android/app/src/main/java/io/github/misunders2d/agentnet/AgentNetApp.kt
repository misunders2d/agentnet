package io.github.misunders2d.agentnet

import android.app.Application
import android.os.Handler
import android.os.Looper
import io.github.misunders2d.agentnet.core.core.Core
import io.github.misunders2d.agentnet.core.core.Listener
import io.github.misunders2d.agentnet.core.core.Session
import org.json.JSONArray
import org.json.JSONObject
import java.io.File
import java.util.concurrent.Executors
import java.util.concurrent.atomic.AtomicBoolean

class AgentNetApp : Application() {
    val repository by lazy { NativeRepository(this) }
}

data class ScreenState(val ready: Boolean = false, val linked: Boolean = false, val busy: Boolean = false,
    val sending: Boolean = false, val chats: List<Chat> = emptyList(), val selected: Chat? = null,
    val messages: List<ChatMessage> = emptyList(), val connection: String = "Opening saved chats…",
    val error: String = "", val transportError: String = "", val frozen: String = "")

/** Three bounded serial lanes keep local reads independent of network sends. No UI polling. */
class NativeRepository(private val app: Application) {
    private val main = Handler(Looper.getMainLooper())
    private val lifecycle = Executors.newSingleThreadExecutor()
    private val reads = Executors.newSingleThreadExecutor()
    private val writes = Executors.newSingleThreadExecutor()
    private val pending = AtomicBoolean(false)
    private val dirty = AtomicBoolean(false)
    private val opening = AtomicBoolean(false)
    private val sending = AtomicBoolean(false)
    private val home = File(app.noBackupFilesDir, "agentnet")
    private val drafts = app.getSharedPreferences("drafts", 0)
    private val options = app.getSharedPreferences("native-options", 0)
    @Volatile private var session: Session? = null
    @Volatile private var foreground = false
    @Volatile private var background = false
    @Volatile var state = ScreenState(); private set
    private val observers = linkedSetOf<(ScreenState) -> Unit>()

    fun observe(observer: (ScreenState) -> Unit) { observers.add(observer); observer(state) }
    fun remove(observer: (ScreenState) -> Unit) { observers.remove(observer) }
    private fun update(change: (ScreenState) -> ScreenState) = main.post {
        state = change(state); observers.toList().forEach { it(state) }
    }
    fun error(message: String) { update { it.copy(error = message.take(500), busy = false) } }
    fun keepConnected() = options.getBoolean("keep-connected", false)
    fun setKeepConnected(value: Boolean) { options.edit().putBoolean("keep-connected", value).apply() }

    fun foreground(value: Boolean) { foreground = value; open(); reconcileConnection() }
    fun background(value: Boolean) { background = value; if (value) open(); reconcileConnection() }

    private fun open() {
        if (session != null || !opening.compareAndSet(false, true)) return
        if (!File(home, "identity.json").exists()) {
            opening.set(false); update { it.copy(ready = true, connection = "Link this phone") }; return
        }
        lifecycle.execute {
            try { install(Core.open(home.absolutePath)) }
            catch (e: Exception) { update { it.copy(ready = true, connection = "Saved chats unavailable",
                error = (e.message ?: "Could not open saved chats").take(500)) } }
            finally { opening.set(false) }
        }
    }

    private fun install(s: Session) {
        session = s
        s.setListener(object : Listener { override fun onChange() { refresh() } })
        // Present SQLite data before any relay operation. A network outage cannot gate first paint.
        readSaved(s)
        reconcileConnection()
    }

    fun link(code: String, name: String) {
        if (session != null || !opening.compareAndSet(false, true)) return
        update { it.copy(busy = true, error = "", connection = "Linking this phone…") }
        lifecycle.execute {
            try { install(Core.link(home.absolutePath, code.trim(), name.trim())) }
            catch (e: Exception) { update { it.copy(ready = true, busy = false, connection = "Link this phone",
                error = (e.message ?: "Link failed; your other device must approve it").take(500)) } }
            finally { opening.set(false); update { it.copy(busy = false) } }
        }
    }

    private fun reconcileConnection() = lifecycle.execute {
        session?.let { s ->
            try { if (foreground || background) s.start() else s.stop(); refresh() }
            catch (e: Exception) { error(e.message ?: "Connection stopped") }
        }
    }

    fun refresh() {
        dirty.set(true)
        if (!pending.compareAndSet(false, true)) return
        main.postDelayed({ reads.execute {
            dirty.set(false)
            session?.let { s -> try { readSaved(s) } catch (e: Exception) { error(e.message ?: "Could not read saved chats") } }
            pending.set(false)
            // At most one read is active or queued; a burst contributes one more snapshot.
            if (dirty.get()) refresh()
        } }, 80)
    }

    private fun readSaved(s: Session) {
        val overview = JSONObject(s.overviewJSON())
        val status = JSONObject(s.statusJSON())
        val link = status.optJSONObject("link")?.optString("state").orEmpty()
        val connection = when {
            link == "pending" -> "Approve this phone on your other device"
            link.isNotEmpty() && link != "linked" -> "Link ${link.replace('_', ' ')} · this phone has not been linked"
            status.optBoolean("connected") -> "Connected"
            foreground || background -> "Connecting · saved chats available"
            else -> "Offline · saved chats available"
        }
        val selected = state.selected
        val threadResult = runCatching { selected?.let {
            JSONObject(if (it.conversation) s.conversationJSON(it.id) else s.threadJSON(it.id))
        } }
        val thread = threadResult.getOrNull()
        val saved = selected?.let { savedDraft(it) }
        val recoveredID = if (selected != null && thread != null && saved != null && SendIntent.isPersisted(saved, selected, thread))
            saved.getJSONObject("request").getString("id") else null
        val chats = Models.chats(overview)
        val messages = if (selected != null && thread != null) Models.messages(thread, selected) else emptyList()
        update { current ->
            // Draft edits and acknowledgements share the UI lane. An older read
            // must never erase a replacement draft made while SQLite was busy.
            val recovered = selected != null && recoveredID != null && acknowledgeDraft(selected, recoveredID)
            current.copy(ready = true, linked = true, chats = chats,
            messages = if (current.selected?.key == selected?.key && threadResult.isSuccess) messages else current.messages,
            connection = connection, frozen = if (current.selected?.key == selected?.key) thread?.optString("frozen").orEmpty() else current.frozen,
            transportError = status.optString("error"),
            error = if (current.selected?.key != selected?.key) current.error
                else if (threadResult.isFailure) threadResult.exceptionOrNull()?.message ?: "Could not open this saved conversation"
                else if (recovered) "" else current.error) }
    }

    fun select(chat: Chat?) { update { it.copy(selected = chat, messages = emptyList(), frozen = "", error = "") }; main.post { refresh() } }
    private fun savedDraft(chat: Chat) = runCatching { JSONObject(drafts.getString(chat.key, "{}")!!) }.getOrDefault(JSONObject())
    private fun acknowledgeDraft(chat: Chat, id: String): Boolean {
        if (!SendIntent.acknowledged(savedDraft(chat), id)) return false
        drafts.edit().remove(chat.key).apply()
        return true
    }
    fun draft(chat: Chat): String = savedDraft(chat).optString("body")
    fun draftFrozen(chat: Chat) = savedDraft(chat).has("request")
    fun draftKind(chat: Chat) = savedDraft(chat).optJSONObject("request")?.optString("kind") ?: "message"
    fun saveDraft(chat: Chat, body: String) {
        if (draftFrozen(chat)) return
        drafts.edit().putString(chat.key, JSONObject().put("body", body).toString()).apply()
    }
    fun discardDraft(chat: Chat) {
        if (sending.get()) return
        drafts.edit().remove(chat.key).apply()
        update { it.copy(error = "") }
    }

    fun send(chat: Chat, body: String, kind: String) {
        if (body.isBlank() || state.frozen.isNotBlank() || !sending.compareAndSet(false, true)) return
        val frozen = SendIntent.freeze(savedDraft(chat), chat, body, kind)
        val data = frozen.getJSONObject("request")
        val id = data.getString("id")
        drafts.edit().putString(chat.key, frozen.toString()).apply()
        update { it.copy(sending = true, error = "") }
        writes.execute {
            try {
                val s = session ?: throw IllegalStateException("Phone is not linked")
                // Flush the stable request ID before the first possible network side effect.
                if (!drafts.edit().putString(chat.key, frozen.toString()).commit()) {
                    throw IllegalStateException("Could not save this message; try again")
                }
                val existing = runCatching {
                    JSONObject(if (chat.conversation) s.conversationJSON(chat.id) else s.threadJSON(chat.id))
                }.getOrNull()
                if (existing == null || !SendIntent.isPersisted(frozen, chat, existing)) {
                    if (chat.conversation) s.sendDMJSON(data.toString()) else s.sendJSON(data.toString())
                }
                // A successful native send has persisted the original ID; transport state comes from the core.
                main.post { acknowledgeDraft(chat, id) }
            } catch (e: Exception) { update { current ->
                if (current.selected?.key == chat.key) current.copy(error =
                    (e.message ?: "Send could not be confirmed; draft retained").take(500)) else current
            } }
            finally { sending.set(false); update { it.copy(sending = false) }; refresh() }
        }
    }

    fun markVisibleRead(ids: List<String>) {
        if (!foreground || ids.isEmpty()) return
        writes.execute { try { session?.markReadJSON(JSONArray(ids).toString()) } catch (_: Exception) { /* original unread flags remain */ } }
    }
}
