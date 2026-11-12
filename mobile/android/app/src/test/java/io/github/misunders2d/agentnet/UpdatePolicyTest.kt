package io.github.misunders2d.agentnet

import org.junit.Assert.*
import org.junit.Test

class UpdatePolicyTest {
    private fun rejected(block: () -> Unit) { try { block(); fail("invalid release input accepted") } catch (_: IllegalArgumentException) {} }
    @Test fun releaseCodesAreBoundedAndMonotonic() {
        assertEquals(8019, UpdatePolicy.code("v0.8.19"))
        assertTrue(UpdatePolicy.code("v1.0.0") > UpdatePolicy.code("v0.999.999"))
        for (v in listOf("v0.08.19", "v0.8.1000", "v1000.0.0", "v0.8.19-preview", "../v0.8.19", "0.8.19")) rejected { UpdatePolicy.code(v) }
    }
    @Test fun releaseSourcesCannotBeChosenByMetadata() {
        assertEquals("v0.8.19", UpdatePolicy.latest("${UpdatePolicy.repository}/releases/tag/v0.8.19"))
        rejected { UpdatePolicy.latest("https://github.com/attacker/agentnet/releases/tag/v0.8.19") }
        rejected { UpdatePolicy.latest("${UpdatePolicy.repository}/releases/tag/v0.8.19?other=1") }
        assertEquals("${UpdatePolicy.repository}/releases/download/v0.8.19/AgentNet-android.apk", UpdatePolicy.downloadURL("v0.8.19", UpdatePolicy.asset))
        rejected { UpdatePolicy.downloadURL("v0.8.19", "../other.apk") }
        for (u in listOf("http://github.com/misunders2d/agentnet/releases/latest", "https://github.com.evil.test/misunders2d/agentnet/releases/latest", "https://github.com/other/project/releases/latest", "https://github.com@evil.test/", "https://release-assets.githubusercontent.com:444/path", "https://github.com/misunders2d/agentnet/releases/latest#fragment")) assertFalse(u, UpdatePolicy.redirect(u))
        assertTrue(UpdatePolicy.redirect("https://release-assets.githubusercontent.com/github-production-release-asset/abc?sig=value"))
    }
    @Test fun unreachableDownloadNamesTheHostAndAnotherNetwork() {
        val url = "https://release-assets.githubusercontent.com/github-production-release-asset/abc?sig=value"
        for (e in listOf(java.net.SocketTimeoutException("failed to connect after 15000ms"), java.net.ConnectException("refused"),
                java.net.NoRouteToHostException("no route"), java.net.UnknownHostException("release-assets.githubusercontent.com"))) {
            val text = UpdatePolicy.unreachable(url, e)!!
            assertTrue(text, text.contains("(release-assets.githubusercontent.com)") && text.contains("mobile data"))
            assertFalse(text, text.contains("sig=") || text.contains("15000"))
        }
        assertNull(UpdatePolicy.unreachable(url, javax.net.ssl.SSLHandshakeException("bad certificate")))
        assertNull(UpdatePolicy.unreachable(url, IllegalStateException("other")))
    }
    @Test fun checksumRequiresOneExactNamedAsset() {
        val digest="a".repeat(64)
        assertEquals(digest,UpdatePolicy.checksum("$digest  AgentNet-android.apk\n"))
        assertEquals(digest,UpdatePolicy.checksum("$digest *AgentNet-android.apk"))
        rejected { UpdatePolicy.checksum("$digest  other.apk") }
        rejected { UpdatePolicy.checksum("$digest  AgentNet-android.apk\n$digest  AgentNet-android.apk") }
        rejected { UpdatePolicy.checksum("a  AgentNet-android.apk") }
    }
    @Test fun installIdentityAndVersionFailClosed() {
        val pkg="io.github.misunders2d.agentnet.preview";val signer=listOf(UpdatePolicy.certificate)
        fun verify(p: String=pkg, s: List<String> = signer, current: Long=3, next: Long=8019, name: String="v0.8.19") =
            UpdatePolicy.validateApk(p,pkg,s,signer,next,current,name,"v0.8.19")
        verify()
        rejected { verify(p="io.github.misunders2d.agentnet") }
        rejected { verify(s=listOf("wrong")) }
        rejected { verify(s=signer+signer) }
        rejected { verify(current=8019) }
        rejected { verify(next=8020) }
        rejected { verify(name="v0.8.19-preview") }
    }

    @Test fun retryOwnsItsPrivateDownloadFile() {
        val directory=java.nio.file.Files.createTempDirectory("agentnet-update-test").toFile()
        try {
            val interrupted=UpdatePolicy.attemptFile(directory)
            val retry=UpdatePolicy.attemptFile(directory)
            assertNotEquals(interrupted,retry)
            interrupted.writeText("old download"); retry.writeText("verified retry")
            assertTrue(interrupted.delete())
            assertEquals("verified retry",retry.readText())
        } finally { directory.deleteRecursively() }
    }

    @Test fun retryReplacesOnlyAnOwnedInactiveConfirmation() {
        val pkg="io.github.misunders2d.agentnet.preview"
        assertTrue(UpdatePolicy.replacePendingSession(pkg,pkg,pkg,true,false))
        assertFalse(UpdatePolicy.replacePendingSession(pkg,pkg,pkg,true,true))
        assertFalse(UpdatePolicy.replacePendingSession(pkg,pkg,pkg,false,false))
        assertFalse(UpdatePolicy.replacePendingSession("other",pkg,pkg,true,false))
        assertFalse(UpdatePolicy.replacePendingSession(pkg,"other",pkg,true,false))
    }

}
