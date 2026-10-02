package main

import "testing"

func TestCodexDetachedLifecycleHookMerge(t *testing.T) {
	cfg, e := mergeHooks(nil, "codex", "'/owned/agentnet' --home '/owned/home' hook codex")
	if e != nil {
		t.Fatal(e)
	}
	hooks := cfg["hooks"].(map[string]any)
	if hooks["SessionEnd"] == nil {
		t.Fatal("native detach hook absent")
	}
	cfg, e = mergeHooks(cfg, "codex", "")
	if e != nil {
		t.Fatal(e)
	}
	if cfg["hooks"] != nil {
		t.Fatal("owned detach hook not removed")
	}
	cfg, e = mergeHooks(nil, "claude", "'/owned/agentnet' --home '/owned/home' hook claude")
	if e != nil {
		t.Fatal(e)
	}
	if cfg["hooks"].(map[string]any)["SessionEnd"] == nil {
		t.Fatal("Claude detached lifecycle hook absent")
	}
}
