package ui

import "testing"

func TestNativeFileDropBridge(t *testing.T) {
	runNodeCheck(t, "testdata/native_file_drop_check.cjs")
}
