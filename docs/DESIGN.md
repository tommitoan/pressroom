# Design notes

Why pressroom is built the way it is, and what was measured before building it. Numbers come from a spike run on macOS (Chrome 154) and in a Linux arm64 container (2 CPUs, 4 GB) with `chromedp/headless-shell`; they are indicative, not benchmarks.

## Decisions

| Question | Decision | Why |
|---|---|---|
| Own service or Gotenberg? | A small own service | The contract stays tiny (HTML in, PDF out), the limits and the security model are explicit and testable, and Gotenberg could replace it later without changing callers. |
| Who builds the HTML? | The caller | pressroom has no templates and no domain knowledge. It never accepts a URL, so there is nothing to fetch. |
| Chromium driver | chromedp | Actions run in order, so request interception is enabled before the document is set. go-rod's router starts asynchronously; in the spike it intercepted 1 of the 5 requests of a hostile page, against 5 of 5 for chromedp. Both produced identical PDFs. |
| Base image | `chromedp/headless-shell` plus fonts | A lean browser: container peak memory was 77 MB without fonts and 137 MB with fonts after ten sequential three-page renders. |
| Network access for rendered pages | none | Every request is intercepted and failed; scripts are disabled. |

## Fonts are part of the contract

The stock `headless-shell` image has no fonts. Rendering a page with Han characters lost every one of them (0 Han characters in the PDF), while Vietnamese still worked through a bundled fallback. With Noto Serif, Noto Serif CJK SC and Noto Sans installed, the PDF showed all Han characters and Vietnamese diacritics and embedded `NotoSerif` and `NotoSerifCJKsc`. So callers should use the font stack `"Noto Serif", "Noto Serif CJK SC", serif`, and the image must carry those fonts: rendered pages cannot download any.

Installing whole packages (`fonts-noto-core`, `fonts-noto-cjk`) grew the image from 518 MB to 754 MB; installing only the needed families is planned.

## How blocking was verified

- A hostile page tried a stylesheet, an image, a web font, a background image, an iframe, a script that rewrites its own text and calls `fetch`, an SVG `onload`, and `file:` URLs.
- With the network allowed and the page served from a logging server, exactly five requests arrived (`style.css`, `pixel.png`, `frame.html`, `bg.png`, `font.woff2`). With interception on, the same five were intercepted and failed.
- The page's script did not run (no `fetch` reached the server, the text stayed unchanged) and `file:///etc/hosts` never appeared in the PDF.
- `data:` URIs are not intercepted and render, so inline images and SVG work.

A lesson worth keeping for tests: when a page is loaded through `Page.setDocumentContent` (what this service does), Chrome itself refuses requests from the `about:blank` page to `127.0.0.1`. A local listener that counts hits therefore shows zero even with the network fully open, and proves nothing. Tests must assert on the interception events, and use a same-origin control to show the counter is able to count.

## Measured

| Item | Result |
|---|---|
| Render time, three A4 pages (cards plus a 60-row table), Linux container | about 0.8 s |
| Peak container memory | 77 MB (no fonts, 3 renders); 137 MB (fonts, 10 renders and the hostile page) |
| PDF size | about 99 KB with fonts |
| Image size | 518 MB stock; 754 MB with whole Noto packages |

## Things callers should know

- Chrome extracts Han glyphs as Kangxi radicals and the letter `Đ` as `Ð` when text is copied out of the PDF. The PDF looks right; copying from it is slightly off.
- Give headings `break-after: avoid`; in the sample a heading was stranded at the bottom of a page.
- A page background set on `body` fills the content area, not the page margins. For a full-bleed colour use `@page { margin: 0 }` and pad the content.

## Not yet verified

- Running the browser as a non-root user with the sandbox on (the spike ran as root with `--no-sandbox`).
- Two concurrent renders and recovery after a browser crash.
- Memory on the target hosting plan.
