# Presenter Slide Decks

Each workshop section has a slide deck alongside its README. The **READMEs
remain the source of truth** for the material; the decks are the projected
version of the same content, and each slide carries the relevant README prose
in its speaker notes.

| Part | Deck | Section README |
|------|------|----------------|
| I | [`01-race-detector/part1-race-detector.slide`](01-race-detector/part1-race-detector.slide) | [`01-race-detector/README.md`](01-race-detector/README.md) |
| II | [`02-execution-tracer/part2-execution-tracer.slide`](02-execution-tracer/part2-execution-tracer.slide) | [`02-execution-tracer/README.md`](02-execution-tracer/README.md) |
| III | [`03-delve/part3-delve.slide`](03-delve/part3-delve.slide) | [`03-delve/README.md`](03-delve/README.md) |

## Presenting

```bash
./slides.sh
```

That installs `present` if needed and serves the repo at
<http://127.0.0.1:3999>. Click through to a deck, then:

| Key | Does |
|-----|------|
| `→` / `←` | next / previous slide |
| **`N`** | **open the presenter-notes window** |
| `F` | full screen |
| `Esc` | slide-grid overview |

The notes window is a **second browser window**, synced to the first. Put it on
your laptop screen and the deck on the projector. Allow pop-ups for
`127.0.0.1:3999` or it won't open.

Everything runs offline — `present` embeds its own CSS and JS, and there are no
CDN references.

## How the decks are built

They're [`present`](https://pkg.go.dev/golang.org/x/tools/cmd/present) files in
Markdown mode: the file starts with `# Title`, each slide starts with `##`, and
each speaker-note line starts with `: `.

Three things are worth knowing before you edit one:

- **Code is quoted from the real source files**, not copy-pasted. For example
  `.code demo/counter.go /^func \(inc \*incrementor\) increment/,/^}/` pulls the
  live function out of the demo. Edit the Go file and the slide follows.
- **Graphics are HTML/SVG files** under each section's `figures/` directory,
  pulled in with `.html figures/<name>.html`. Shared styling lives in
  [`slides-common/figures.html`](slides-common/figures.html), which each deck
  includes once on its first content slide.
- **Markdown tables don't work.** `present`'s Markdown renderer is CommonMark
  with no table extension, so a pipe table renders as literal text. Tables in
  these decks are `.html` figures using the `.grid` / `.sched` classes.

Two more footguns the checker will catch for you: the first line must start with
`# ` (hash *plus a space*, or the file silently parses as legacy present), and
no body line may start with a `.` (it's read as a directive — write `&hellip;`
instead of a leading `...`).

## Validating

```bash
./scripts/check_slides.sh
```

Confirms every deck parses, every referenced figure and source file exists, and
none of the footguns above have crept in. Worth running after editing a README
and porting the change across.

## Adding a graphic

Create `<section>/figures/<name>.html` containing a `<div class="fig">` wrapper,
then reference it with `.html figures/<name>.html`. The shared stylesheet gives
you:

- `.term` — a dark terminal panel; `.hl` / `.hl-red` / `.hl-cyan` highlight a
  line inside it, and `.dim` / `.amber` / `.green` / `.cyan` color a span
- `.annotated` — terminal on the left, `.callout` boxes on the right
- `.cards` / `.card` — a row of cards, tinted with `.red` / `.amber` / `.green`
  / `.purple`
- `.grid` — a table; `.sched` — a schedule table with a time gutter
- `.compare` — before/after code panels
- `.pill`, `.note`, `.lede`

Inline SVG works and is the right tool for anything with arrows or timelines —
see `01-race-detector/figures/happens-before.html` and
`02-execution-tracer/figures/starvation.html`.
