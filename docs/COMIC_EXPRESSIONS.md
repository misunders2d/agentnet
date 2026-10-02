# Comic expression prototype (MEL-434)

This representative prototype extends the existing selectable Comic lens. Classic
remains the default compact view; both show the same conversation, history,
authors, attachments and review actions. It implements the prototype part of
[DECISIONS.md §4](DECISIONS.md#4-comic-avatars--visual-expressions-mel-434-specification),
using the previously reviewed brief rather than choosing a generation provider.

## What the prototype renders

Comic panel faces use provisional platform Unicode emoji, looked up from the
message's existing explicit `emotion`. They are expression placeholders, not a
generated matching character set, a portrait, or proof of identity. Platform fonts
can draw them differently. The existing author name and human/agent attribution
remain in the panel footer and full-message details; faces grant no authority.

| Token | Provisional face | Meaning |
| --- | --- | --- |
| `neutral` | 😐 | No specific expression selected |
| `happy` | 🙂 | Happy |
| `sad` | 😢 | Sad |
| `focused` | 🧐 | Focused |
| `curious` | 🤔 | Curious |
| `concerned` | 😟 | Concerned |
| `celebrating` | 🥳 | Celebrating |

This is the seven-token proposal for review, not a finalized expanded vocabulary.
Missing, unknown and malformed values render neutral, including old-peer turns.
The renderer never derives emotion from text, delivery, execution or review state.
For example, a happy face can accompany a failed result: the actual failure stays
in its status caption. No protocol change or extra model call is introduced.

Reply references use only the exact message ID already in the current thread.
Clicking a present parent uses the existing Comic page/focus navigation. Deleted
parents say “(deleted message)”; absent parents say they are not shown here. No
history fetch, identity lookup or authority expansion follows the reference.

Long text and code remain selectable DOM text. Comic clips the panel preview and
keeps **Read in full**; the same dialog exposes existing attachment **Open** and
download controls. Task decisions keep their existing confirmation and explicit
checkbox gate. Offline and queued captions describe transport facts, independently
of expressions. Cover, page navigation, keyboard arrows and All pages remain.

## Candidate lifecycle for a future matching character set

The requested future design is one cached set generated at approved enrollment or
an explicit avatar change, never generation per message. It is not implemented by
this prototype. Before implementation, agree these controls:

1. The owner selects or supplies an avatar concept and previews the entire fixed
   expression set, with a clear option to decline or keep the existing set.
   Sharing a likeness or prompt with a provider requires the owner's consent.
2. Approval fixes an immutable asset-set version and content digest. Cache the
   approved bytes locally; messages refer to a known version/expression rather
   than asking a provider to draw a new image.
3. An avatar change creates a new approved version. Keep old versions needed by
   retained history so a new portrait does not silently rewrite earlier turns.
   Missing assets, unavailable versions or unsupported expressions use a neutral
   local placeholder while preserving the author and readable history.
4. Expression choice belongs to the sender: a human may choose one or use neutral;
   the already-running agent emits its choice under the negotiated message
   contract. Do not add a sentiment classifier or status-to-emotion mapping.
5. Asset transport, cache bounds, deletion and retention policy still need a
   concrete design. Do not embed private prompts, provider credentials or likeness
   metadata in message logs. Treat supplied assets as data, not executable content.

Provider, price, generation consent, likeness rights, retention and provider
privacy terms remain unresolved. Review redistribution and font/asset licensing
before shipping a bundled character set; this prototype adds no font, artwork
bundle, CDN or external provider integration and makes no licensing determination.

## Motion and acceptance boundary

Expression faces are static. This change adds no animation. Existing page turns
honor `prefers-reduced-motion`; Classic also provides a static compact view.
Future balloon entrances, expression crossfades or speaker handoffs need explicit
motion choices and tests for hidden/offscreen work, focus, and reduced motion.
No idle/active CPU, memory, latency or GPU measurement was made, and no quantified
resource or “lightweight animation” claim is supported here.

`internal/ui/testdata/comic_expression_check.cjs` checks production rendering with a
small DOM stand-in. `comic_prototype_check.cjs` loads the production page, styles
and renderer in an isolated browser with synthetic providers. Its representative
conversation includes human and agent turns, a reply branch, all seven examples,
old/unknown values, long code, a downloadable attachment, offline state, and a
gated human-review decision at 1280 and 390 pixels. It compares Classic and Comic
and checks keyboard/grid navigation, reduced motion and settled static rendering.
Screenshots and run evidence belong in private temporary storage, not this repo.

These checks prove rendering and synthetic interaction only. They do not prove
live encrypted delivery, model generation, provider compliance, production
performance, or final release acceptance. Root retains independent review and
the release decision. Art direction, vocabulary, provider/cost, consent and motion
strength remain product decisions; Comic stays selectable in this prototype.
