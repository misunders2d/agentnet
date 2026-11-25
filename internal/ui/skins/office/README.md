# Office (built in)

Office is AgentNet's default skin on a new profile: Comic's interface with
its own skin layer (`overlay/`, the slots in `web/src/skin/README.md`). It
is plain and quiet like a workplace messenger: white pages, thin grey
hairlines, a dark neutral rail, the device's own interface font.

`overlay/` is the one maintained source. It came from the agentnet-skins
collection (`skins/office/overlay` at 84adba27, Apache-2.0, same owner); the
collection no longer publishes Office once a release bundles it.

`web/build.sh` builds it into `static/skins/office/` right after Comic, so a
change to Comic's source always rebuilds both. It writes `LICENSES.txt` and
`SHA256SUMS` here; `TestBuiltinSkinServedBytesMatchBuild` checks the
embedded package against them. The build is deterministic: running it again
leaves `git diff` empty.

Preferences keep the keys the gallery version used (`office`,
`office.theme`), so a browser that had Office from the gallery keeps its
theme and settings when it moves to the built-in one.
