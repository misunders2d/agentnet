package ui

import (
	"os"
	"testing"
)

func TestNativeActivityWorkingCardModel(t *testing.T) {
	runNodeCheck(t, "testdata/native_activity_check.mjs")
}

func TestNativeActivityWorkingCardSSR(t *testing.T) {
	if _, err := os.Stat("web/node_modules/esbuild"); err != nil {
		t.Skip("SSR requires the existing pinned web build dependencies")
	}
	runNodeCheck(t, "testdata/native_activity_ssr_check.cjs")
}
