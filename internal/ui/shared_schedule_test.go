package ui

import "testing"

func TestSharedScheduleModel(t *testing.T) {
	runNodeCheck(t, "testdata/shared_schedule_model_check.mjs")
}

func TestSharedScheduleDOM(t *testing.T) {
	runNodeCheck(t, "testdata/shared_schedule_dom_check.mjs")
}
