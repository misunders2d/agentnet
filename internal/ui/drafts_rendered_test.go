package ui

import "testing"

func TestComicDraftDestinationsRendered(t *testing.T) {
	t.Setenv("P11_SKINS", "comic")
	t.Setenv("AGENTNET_SCREENSHOTS", t.TempDir())
	t.Setenv("P11_DRAFTS_ONLY", "1")
	t.Log(browserCheck(t, "testdata/chattopics_rendered.cjs"))
}

func TestComicMentionButtonRendered(t *testing.T) {
	t.Setenv("P11_SKINS", "comic")
	t.Setenv("AGENTNET_SCREENSHOTS", t.TempDir())
	t.Setenv("P11_MENTIONS_ONLY", "1")
	t.Log(browserCheck(t, "testdata/chattopics_rendered.cjs"))
}

func TestRemoteStopRendered(t *testing.T) {
	t.Setenv("P11_SKINS", "comic")
	t.Setenv("AGENTNET_SCREENSHOTS", t.TempDir())
	t.Setenv("P11_STOP_ONLY", "1")
	t.Log(browserCheck(t, "testdata/chattopics_rendered.cjs"))
}

func TestRapidRemoteAskFeedbackRendered(t *testing.T) {
	t.Setenv("P11_SKINS", "comic")
	t.Setenv("P11_RAPID_ASK_ONLY", "1")
	t.Setenv("AGENTNET_SCREENSHOTS", privateScreenshots(t))
	t.Log(browserCheck(t, "testdata/chattopics_rendered.cjs"))
}
