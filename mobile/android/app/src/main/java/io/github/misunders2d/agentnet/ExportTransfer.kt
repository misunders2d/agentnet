package io.github.misunders2d.agentnet

import java.io.File
import java.io.FileOutputStream
import java.util.UUID

/** One bounded export staged privately, then written only to a human-chosen SAF URI. */
class ExportTransfer(private val directory: File) : AutoCloseable {
    companion object {
        const val MAX_SIZE = 100L * 1024 * 1024
        const val MAX_CHUNK = 48 * 1024
        fun basename(value: String): String = value.replace('\\', '/').substringAfterLast('/').filter { it >= ' ' && it != '\u007f' }.take(200).ifBlank { "download" }.let { if (it == "." || it == "..") "download" else it }
        fun mime(value: String): String = if (value in setOf("image/png", "image/jpeg", "image/gif", "image/webp", "image/avif", "application/pdf", "text/plain", "application/zip", "audio/mpeg", "video/mp4")) value else "application/octet-stream"
    }
    data class Ready(val file: File, val name: String, val mime: String)
    private var id: String? = null
    private var file: File? = null
    private var stream: FileOutputStream? = null
    private var expected = 0L
    private var written = 0L
    private var name = "download"
    private var mime = "application/octet-stream"
    private var finished = false
    @Synchronized fun begin(name: String, mime: String, size: Long): String {
        check(id == null) { "Another download is awaiting completion." }
        require(size in 0..MAX_SIZE) { "Download exceeds the file limit." }
        directory.mkdirs()
        val f = File.createTempFile("export-", ".tmp", directory)
        file = f; stream = FileOutputStream(f); expected = size; written = 0
        this.name = basename(name); this.mime = Companion.mime(mime); finished = false
        return UUID.randomUUID().toString().also { id = it }
    }
    @Synchronized fun append(token: String, bytes: ByteArray): Long {
        check(token == id && !finished) { "Unknown or completed download." }
        require(bytes.size <= MAX_CHUNK && written + bytes.size <= expected) { "Invalid download chunk." }
        stream!!.write(bytes); written += bytes.size
        return written
    }
    @Synchronized fun finish(token: String): Ready {
        check(token == id && !finished) { "Unknown or completed download." }
        check(written == expected) { "Incomplete download." }
        stream!!.fd.sync(); stream!!.close(); stream = null; finished = true
        return Ready(file!!, name, mime)
    }
    @Synchronized fun cancel(token: String) { check(token == id) { "Unknown download." }; close() }
    @Synchronized override fun close() { stream?.close(); stream = null; file?.delete(); file = null; id = null; finished = false }
}
