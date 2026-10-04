# Retired: real-binary journeys of the bundled app

These world scripts and journeys drove the bundled app (`static/default.html`,
`app.js`, `lenses.js`, `app.css`) through a real `agentnet` binary. No release
serves that app any more: the UI host mounts skin packages only, and Comic is
the default (docs/UI_SKINS.md, docs/DECISIONS.md §9). They stopped matching
the product when the new messenger became the default page, and were retired
when the bundled app left the binary. Nothing runs them, and they are not
expected to pass where they are.

They are kept, not deleted, because the Classic and Zoom skins are being
built as packages from the bundled app. To bring one back, retarget it at
that package: choose the skin (`?skin=classic`, or the saved choice), drive
the page inside the skin's shadow root, check the package's served files
(`/assets/skins/<id>/...`) instead of `/assets/app.js`, and move it back to
`internal/ui/testdata/` with its world script (paths inside assume that).
`pwa_installed_journey_test.go.txt` is the opt-in Go driver of
`pwa_installed_world.sh`, renamed so the Go tool ignores it.

Fixture checks that serve the bundled app themselves (`bundled_app.cjs`,
`onboarding_rendered_test.go`, `typing_combined_journey_test.go` and the
other `default.html` fixtures) still run against its sources in place.
