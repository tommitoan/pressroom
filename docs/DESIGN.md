# Design notes

Why pressroom is built the way it is, and what was measured before building it. Numbers come from a spike run on macOS (Chrome 154) and in a Linux arm64 container (2 CPUs, 4 GB) with `chromedp/headless-shell`; they are indicative, not benchmarks.

## Decisions

| Question | Decision | Why |
|---|---|---|
| Own service or Gotenberg? | A small own service | The contract stays tiny (HTML in, PDF out), the limits and the security model are explicit and testable, and Gotenberg could replace it later without changing callers. |
| Who builds the HTML? | The caller | pressroom has no templates and no domain knowledge. It never accepts a URL, so there is nothing to fetch. |
| Chromium driver | chromedp | Actions run in order, so request interception is enabled before the document is set. go-rod's router starts asynchronously; in the spike it intercepted 1 of the 5 requests of a hostile page, against 5 of 5 for chromedp. Both produced identical PDFs. |
| Base image | `chromedp/headless-shell` (pinned to 151.0.7922.109) plus fonts | A lean browser: container peak memory was 77 MB without fonts and 137 MB with fonts after ten sequential three-page renders. |
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

## Verified in the service

The same rules now run in the service and are covered by tests that start a real browser (`make e2e`). Results below are from macOS with Chrome 154 and with Playwright's chrome-headless-shell; both pass the whole suite three times in a row.

- The hostile page: stylesheet, background image, web font, image and iframe requests are all intercepted, and a script that calls `fetch` or an `onload` handler produces no request. An inline `data:` image is not refused.
- A three-page Han and Vietnamese sample renders in about 60 to 100 ms once the browser is running (0.4 to 1.1 s including the first start). Page-number footers show `n of 3` on every page.
- Four concurrent renders keep their own content apart.
- A render that exceeds its deadline returns `context.DeadlineExceeded`, closes its tab, and the browser keeps working.
- Killing the browser process is detected on the next request and replaced by a new browser; no request fails in between in the test.
- A full queue is turned away at once (`ErrBusy`, mapped to `429`).
- Through the real HTTP handler: a burst of 10 requests with 1 running and 1 queued gave 2 renders and 8 rejections, the slowest rejection in 81 ms; `/ready` stayed 200 and the next render worked.
- Through the real HTTP handler: the hostile page returns a PDF with every request intercepted; a 40 ms deadline gives `504 render_timeout` and the browser stays healthy; malformed JSON, unknown fields, bad options, a wrong or missing token and an oversized body are refused without echoing the token and without harming the service.
- After a browser killed without being reaped, a restart and a close: no defunct child process and no leftover `chromedp-runner*` profile directory (a control confirms the directory exists while the browser runs). After 15 renders and a timed-out one, only the initial tab is open.
- `SIGTERM` during a render: the request completed with 200 and a PDF, the process exited with code 0 right after, and no browser was left running.

Findings that shaped the code:

- **No separate browser context per render.** Opening a tab in a new browser context fails in Chrome's new headless mode with "no browser is open", so each render uses its own tab in the default context. Scripts are off and every request is refused, so a page has nothing to store or fetch; the tab is closed after the render.
- **Interception events are the evidence** (see above), so the engine takes an optional callback that receives each refused URL; production leaves it unset and only a count is logged at debug level.
- **Cancelled requests used to leak tabs.** chromedp creates a tab with the context it is given; when that context was cancelled in flight the browser still opened the tab but chromedp never learned its id, so it was never closed. In a loop of 60 renders cancelled after 1 to 20 ms, 47 tabs stayed open on Chrome and 4 on headless-shell. The tab is now created with a context that does not depend on the request, the request's cancellation is attached afterwards, and `closeTab` checks that the tab is gone and repeats the close if not (chromedp's own close occasionally leaves a just-created tab open without reporting an error). Both leak tests fail without the fix and leave exactly one tab with it, on macOS Chrome, headless-shell and Linux headless-shell.
- **Printing waits for the page's load event.** Without it a fast machine could print before the parser had issued the page's requests, so the evidence of what was refused varied from run to run (the hostile-page test failed about one run in three on Linux). The wait costs nothing measurable. Inline `data:` images were never lost, with or without it (40 renders per browser).
- **Header and footer templates.** When only one of them is set, the other is replaced with an empty element, otherwise Chrome prints its default date and title banner.
- **Graceful shutdown.** `http.Server.Shutdown` makes `Serve` return at once, so `serve()` waits for the drain to finish before the browser is closed; the drain allows the render timeout plus 5 s. It is a function of its own so tests can run it with a slow handler.
- **Header limit.** `net/http` reads 4 KiB beyond `MaxHeaderBytes` before answering 431, so the effective limit is about 20 KiB.

## The image

Measured on a Linux arm64 container (Docker via Colima, 4 GB), running locked down: non-root, `--read-only`, `--tmpfs /tmp`, `--cap-drop ALL`, `--security-opt no-new-privileges`.

| Item | Result |
|---|---|
| Image size | 636 MB (pinned headless-shell base plus 60 MB of fonts and an 8 MB binary) |
| Fonts | only Noto Serif, Noto Sans (four styles each) and Noto Serif CJK SC (Regular and Bold) are kept; installing and pruning share one layer. The unpruned packages add about 136 MB |
| Sample document through HTTP | 3 pages, 27 Han characters, Vietnamese diacritics; `NotoSerif`, `NotoSerifCJKsc` and `NotoSans` embedded; 37 to 51 ms warm |
| Memory | 83 MiB after the first renders, 88 MiB after 20 more, 103 MiB peak (cgroup `memory.peak`) |
| Burst of 12 parallel requests (concurrency 2, queue 4) | 6 times 200, 6 times 429; `/ready` stayed 200 |
| Browser processes | six, all running as UID 10001 |
| Build from a copy of only the tracked files | works; `/ready` 200 |

**The sandbox cannot be on in the image.** As a non-root user with Docker's default seccomp profile the browser exits at start with "No usable sandbox" (`/ready` stays 503). With `--security-opt seccomp=unconfined` the same image starts with the sandbox on, so the cause is user namespaces being blocked, not the image. The headless-shell build has no setuid `chrome-sandbox` helper to fall back on, and a hosted platform cannot change the seccomp profile, so the image sets `CHROMIUM_NO_SANDBOX=true`. The remaining controls are listed in the README's security model.

`font-family` resolution: `fc-match` maps `Noto Serif`, `Noto Serif CJK SC`, `Noto Sans` and the generic `serif` and `sans-serif` to the bundled files; `docker/fonts.conf` adds the generic mappings.

## Not yet verified

- The image on amd64 (built and measured on arm64 only).
- Running the Go browser tests (`make e2e`) inside the container; the HTTP-level checks cover the same paths.
- The CI browser job: the runner's sandbox and CJK fonts are not confirmed, so it is allowed to fail.
- Memory and throughput on the target hosting plan.
