# Design

Write the design the Guildmaster will decide on before the task is planned or built.
Your `brief` input says what the design must show and why; its first line, `Surface:`, names what the change shows the Guildmaster: `web-page`, `cli`, `chat` or `none`.
The `context` inputs, when given, are the latest grill notes, research notes and spec.
Make the design self-contained and short enough to read in one decision.
Set out the options and the trade-offs between them, and say which option you recommend.
List every decision the Guildmaster still has to make under `## Open questions`, or write "None": each starts on a line `**Qn. <title>**`, numbered from Q1 with no gaps, and has an `Options: (a) <one answer>; (b) <another>.` line and a `Recommended: (<letter>). <why>` line, the spec's shape.
Nothing else may come before the first `**Q1.` line: no sentence, no `### Q1.` heading, no list marker such as `- **Q1.`, and no explanation after "None".
The design ask shows each question as a card with its recommended option first and marked, and Guildhall refuses a design with no such section, one in another shape, or a `Recommended:` line that does not start with one of its option letters.
Write the required `design` port as a UTF-8 Markdown document at `design.md` in your working directory: it holds the written decisions.
The prompt's Strict sections block shows a valid and an invalid `## Open questions` example, with the error the invalid one gets.
Submit your result with `guildhall result submit`, as the Result template says: it refuses a design or mock-up in another shape with the reason intake would give, so fix it and submit again until it prints `ok`.
Change nothing in the repository; scratch files in your attempt directory are allowed.

## Mock-ups

Show the change as it will really look, as the brief's surface asks; the required outputs below name the file.

- `web-page`: write `mockup.html`, one self-contained HTML file the Guildmaster opens from the design ask.
  Build it from the real task page: copy `internal/web/testdata/task.golden.html`, or `internal/web/testdata/task.answering.golden.html` when the change touches the Guildmaster's forms.
  These are the real page template and CSS rendered with a fixture, the CSS already inlined.
  Edit the copy's markup and sample data with your file tools to show the change, keeping the page's own classes and styles.
  Lay `mockup.html` out as two `<iframe srcdoc>` frames, side by side and wrapping on a narrow screen: a desktop frame 1280px wide and a phone frame 390px wide, each holding the edited page, so the page's own `@media (max-width:760px)` rule lays out the phone frame.
  Escape each page for its `srcdoc` attribute: `&` as `&amp;` and `"` as `&quot;`.
  Use no script and no `on…=` event attribute, and load nothing from the network: only inline styles and `data:` images, with no `http:`, `https:` or `//` URL in `src`, `href`, `url(` or `@import`; point the golden page's external links, such as pull request links, at `#`.
- `cli` or `chat`: write `mockup.md`, the exact sample command-line output or chat reply, with sample data, in fenced code blocks, a short line before each saying what it shows.
- `none`: write `design.md` alone; sketch in it only where words fall short.
