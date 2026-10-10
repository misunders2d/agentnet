package io.github.misunders2d.agentnet

import org.junit.Assert.*
import org.junit.Test
import java.nio.file.Files

class ExportTransferTest {
    @Test fun savesOnlyCompleteExactTransferAndCleansPlaintext() {
        val directory = Files.createTempDirectory("export-test").toFile()
        try {
            ExportTransfer(directory).use { transfer ->
                val token = transfer.begin("../../secret.txt", "text/plain", 3)
                assertEquals(2, transfer.append(token, byteArrayOf(1, 2)).toInt())
                assertThrows(IllegalStateException::class.java) { transfer.finish(token) }
                assertThrows(IllegalStateException::class.java) { transfer.append("unknown", byteArrayOf(3)) }
                transfer.append(token, byteArrayOf(3))
                val ready = transfer.finish(token)
                assertEquals("secret.txt", ready.name)
                assertArrayEquals(byteArrayOf(1, 2, 3), ready.file.readBytes())
                assertThrows(IllegalStateException::class.java) { transfer.append(token, byteArrayOf()) }
                assertThrows(IllegalStateException::class.java) { transfer.begin("another", "text/plain", 1) }
            }
            assertEquals(0, directory.listFiles()!!.size)
        } finally { directory.deleteRecursively() }
    }
    @Test fun boundsEachChunkTotalAndFilename() {
        val directory = Files.createTempDirectory("export-test").toFile()
        try {
            ExportTransfer(directory).use { transfer ->
                assertThrows(IllegalArgumentException::class.java) { transfer.begin("large", "text/plain", ExportTransfer.MAX_SIZE + 1) }
                val token = transfer.begin("x", "text/html", 1)
                assertThrows(IllegalArgumentException::class.java) { transfer.append(token, byteArrayOf(1, 2)) }
                assertThrows(IllegalArgumentException::class.java) { transfer.append(token, ByteArray(ExportTransfer.MAX_CHUNK + 1)) }
                transfer.append(token, byteArrayOf(9))
                assertEquals("application/octet-stream", transfer.finish(token).mime)
                transfer.cancel(token)
                assertEquals(0, directory.listFiles()!!.size)
            }
            assertEquals("download", ExportTransfer.basename(".."))
            assertEquals("badname", ExportTransfer.basename("bad\u0000name\n"))
            assertEquals(200, ExportTransfer.basename("x".repeat(300)).length)
        } finally { directory.deleteRecursively() }
    }
}
