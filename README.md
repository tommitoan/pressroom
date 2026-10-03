# pressroom

[![CI](https://github.com/tommitoan/pressroom/actions/workflows/ci.yml/badge.svg)](https://github.com/tommitoan/pressroom/actions/workflows/ci.yml)
[![Go](https://img.shields.io/badge/go-1.26%2B-00ADD8?logo=go&logoColor=white)](https://go.dev/)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
[![Status](https://img.shields.io/badge/status-early%20development-orange.svg)](#roadmap)

**A small, private HTML-to-PDF microservice written in Go.** Send one self-contained HTML document, get back a PDF printed by headless Chromium. Pages render with scripts off and every network request blocked, behind a bearer token and strict limits. It knows nothing about what it prints, so any service can reuse it.

```sh
curl -sS -X POST http://localhost:8080/v1/pdf \
  -H "Authorization: Bearer $PRESSROOM_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"html":"<!doctype html><h1>Hello</h1>","options":{"paper":"A4"}}' \
  -o hello.pdf
```

> **Status: early development.** The HTTP API, authentication, validation, limits and the Chromium rendering engine are done and tested end to end. A Docker image with the browser, fonts and a non-root user is included. Outside the image, without a Chrome or Chromium binary on the host, `POST /v1/pdf` answers `503 unavailable` and `GET /ready` answers `503`, on purpose, so the service never pretends to render.

## Why

Printing a page to PDF well needs a real browser engine: CSS paged media, web fonts, Han characters and Vietnamese diacritics, repeated table headers, page numbers. A browser is heavy, so it does not belong inside the application that wants the PDF. pressroom puts it behind a small HTTP API with a narrow, hardened contract:

- the **caller owns the document**: it builds the HTML (templates, data, language) and pressroom only prints it;
- the **service owns the risk**: Chromium, fonts, memory, timeouts and isolation live in one place.

It started as the export service for a personal web app, and was kept generic on purpose.

## Features

| | |
|---|---|
| Done | `POST /v1/pdf` with strict JSON, defaults and range checks for paper, margins, scale, orientation, header and footer |
| Done | Bearer-token authentication, constant-time comparison, token length enforced at startup |
| Done | Structured JSON logs with a request id; request bodies, queries and tokens are never logged |
| Done | Fixed error contract (`{"error", "code"}`), `Retry-After` on overload, errors never echo the request |
| Done | `GET /health` and `GET /ready`, graceful shutdown, panic recovery, per-render deadline |
| Done | Renderer behind an interface, with an honest "unavailable" implementation and a test double |
| Done | Chromium engine: scripts disabled, every request intercepted and failed, one tab per render, browser started on first use and restarted if it dies |
| Done | Concurrency limit with a bounded queue (fast `429` instead of waiting), graceful drain of in-flight renders on shutdown |
| Done | Docker image with the browser, only the needed Noto fonts (Han and Vietnamese), a non-root user |

## How it works

```
 caller (private network)
    |  POST /v1/pdf   { "html": "...", "options": { ... } }
    |  Authorization: Bearer <token>
    v
+--------------------------- pressroom ----------------------------+
|  request log -> recover -> bearer auth -> validate -> Renderer   |
|                                                         |        |
|                                          Chromium engine         |
|                                       scripts off, network off   |
+------------------------------------------------------------------+
    |  200 application/pdf
    v
 caller
```

The HTTP layer only depends on a small interface, so the engine can be tested, replaced or faked without a browser:

```go
type Renderer interface {
    Render(ctx context.Context, req Request) ([]byte, error)
    Ready(ctx context.Context) error
}
```

## API at a glance

Full contract: [docs/API.md](docs/API.md).

| Endpoint | Purpose |
|---|---|
| `POST /v1/pdf` | render HTML to PDF (bearer token required) |
| `GET /health` | the process is up |
| `GET /ready` | a render can start |

Request body:

```json
{
  "html": "<!doctype html>...",
  "options": {
    "paper": "A4",
    "landscape": false,
    "margins_mm": { "top": 15, "right": 12, "bottom": 18, "left": 12 },
    "print_background": true,
    "prefer_css_page_size": true,
    "scale": 1,
    "header_html": "<span></span>",
    "footer_html": "<div style=\"font-size:8px;width:100%;text-align:center\"><span class=\"pageNumber\"></span> / <span class=\"totalPages\"></span></div>"
  }
}
```

Only `html` is required. Errors are always JSON and never contain the request:

| Status | `code` | Meaning |
|---|---|---|
| 400 | `invalid_json`, `html_required`, `invalid_options` | the request cannot be used |
| 401 | `unauthorized` | missing or wrong token |
| 413 | `payload_too_large` | body over the limit |
| 429 | `busy` | too many renders in progress; has `Retry-After` |
| 503 | `unavailable` | the renderer cannot run; has `Retry-After` |
| 504 | `render_timeout` | the render took too long |
| 500 | `render_failed`, `internal_error` | anything else |

### A Go client

[`examples/client`](examples/client) holds a small client that uses only the Go standard library. Copy `client.go` into the service that needs PDFs:

```go
c := client.New(os.Getenv("PRESSROOM_URL"), os.Getenv("PRESSROOM_TOKEN"))
pdf, err := c.PDF(ctx, html, &client.Options{Paper: "A4", FooterHTML: footer})

var apiErr *client.APIError
if errors.As(err, &apiErr) {
    log.Printf("pressroom said %s (%d)", apiErr.Code, apiErr.Status)
}
```

It returns the service's error code in a typed error, keeps options you leave unset at the service defaults (a margin of `0` and `print_background: false` are sent when you ask for them), and retries only `429 busy` and `503 unavailable`, after the `Retry-After` the service sends.

### A contract test

[`examples/client/contract_test.go`](examples/client/contract_test.go) checks, over plain HTTP, what a caller can rely on: the probes, authentication, a valid document with and without every option, remote content that is not fetched, every error code and that the answers never repeat the request, and the client itself against the service. It skips unless `PRESSROOM_URL` and `PRESSROOM_TOKEN` are set; the CI image job runs it against the built image. Copy it next to `client.go` in a consumer and run it after changing either side.

```sh
make docker-build && make docker-run              # in one terminal
PRESSROOM_URL=http://localhost:8080 PRESSROOM_TOKEN="$PRESSROOM_TOKEN" make contract
```

## Security model

pressroom renders HTML written by someone else, so the page is treated as untrusted.

**Enforced by the code today**

- **Private by design.** Run it on a private network with no public domain. One shared bearer token protects the render endpoint, compared in constant time; it must be at least 16 characters and the service refuses to start without it.
- **Nothing stored, nothing logged.** Logs hold method, path, status, duration and sizes; never bodies, queries or tokens. Tests assert this.
- **Errors say nothing about the input.** Messages are fixed text.
- **Bounded input.** Strict JSON, unknown fields rejected, body size limit, option ranges, a deadline per render.
- **No scripts, no network.** JavaScript is disabled and every request the page makes (stylesheets, images, fonts, frames) is intercepted and failed before it leaves the browser. The service never loads a URL. `data:` URIs work, so logos and small graphics can be inlined. An integration test renders a hostile page and asserts which requests were intercepted (see [docs/DESIGN.md](docs/DESIGN.md) for why the evidence comes from interception events rather than a listening socket).
- **Bounded load.** At most `RENDER_CONCURRENCY` renders run at once, `RENDER_QUEUE` more may wait, and everything beyond that gets `429` immediately. A timed-out render closes its tab.
- **Contained.** Each render gets its own tab in a shared browser and nothing is kept between renders. If the browser dies, the next request starts a new one.

**The Chromium sandbox is off in the image, on purpose**

The sandbox needs user namespaces, and Docker's default seccomp profile blocks them; hosted platforms do not let a service change that. With the sandbox on, the browser in the image cannot start (checked: it starts when seccomp is relaxed). The image therefore sets `CHROMIUM_NO_SANDBOX=true` and relies on the other controls: a non-root user (UID 10001), scripts disabled, every page request refused, no files or network reachable from a page, a private network and a bearer token. If you control the host's seccomp profile, set `CHROMIUM_NO_SANDBOX=false`. Outside the image the default is `false`.

For defence in depth run the container with `--read-only --tmpfs /tmp --cap-drop ALL --security-opt no-new-privileges`; the image works with all of them.

## Configuration

| Variable | Default | Meaning |
|---|---|---|
| `PRESSROOM_TOKEN` | required | shared secret, at least 16 characters |
| `PORT` | `8080` | listen port |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `MAX_BODY_BYTES` | `2097152` | request body limit (1024 to 67108864) |
| `RENDER_TIMEOUT` | `20s` | per-render time limit (1s to 2m) |
| `CHROME_PATH` | auto-detect | Chrome, Chromium or chrome-headless-shell binary |
| `CHROMIUM_NO_SANDBOX` | `false` (`true` in the image) | disable the Chromium sandbox; see the security model |
| `RENDER_CONCURRENCY` | `2` | renders that run at once (1 to 16) |
| `RENDER_QUEUE` | `4` | renders allowed to wait for a free slot (0 to 100); more get `429` |

Copy `.env.example` for a starting point and never commit a real token. Generate one with `openssl rand -hex 32`.

## Run it

To run it as a private service on Railway see [docs/DEPLOY.md](docs/DEPLOY.md), which also has a troubleshooting table (fonts, memory, `429`, `504`).

### With Docker

The steps are the same on macOS, Ubuntu and Fedora; only installing Docker differs.

```sh
export PRESSROOM_TOKEN="$(openssl rand -hex 32)"
make docker-build        # docker build -t pressroom:local .
make docker-run          # serves on localhost:8080
```

The image is about 640 MB: the pinned `chromedp/headless-shell` base (Chromium 151), 60 MB of fonts (Noto Serif, Noto Sans, Noto Serif CJK SC) and the service binary. With a running browser the container used about 90 MB after twenty renders and peaked at 103 MiB.

### Without Docker

Needs Go 1.26 or newer and a Chrome or Chromium binary. Only installing Go and the browser differs between systems.

```sh
export PRESSROOM_TOKEN="$(openssl rand -hex 32)"
export CHROME_PATH="/path/to/chrome"   # omit if chrome or chromium is on PATH
make run                               # go run ./cmd/pressroom
curl -s localhost:8080/health
curl -s localhost:8080/ready           # 200 once the browser answers
```

The browser starts on the first render (or the first `/ready`), so the first request pays about half a second more. Han characters need a CJK font on the host (the image has one); without it they print as empty boxes. In your pages, use the font stack `"Noto Serif", "Noto Serif CJK SC", serif`.

## Development

```sh
make check               # gofmt, go vet, tests with the race detector, build
make test                # tests only, no browser needed
make e2e                 # also drives a real browser; needs CHROME_PATH
```

The default tests use a fake renderer, so they need no browser. `make e2e` runs the same suite with `PRESSROOM_E2E=1` against the real engine, directly and through the HTTP handler: three-page Han and Vietnamese sample, page-number footers, margins, a hostile page, request timeout, a burst above the queue, concurrent renders, bad and oversized requests, a killed browser, and no leftover processes, profiles or tabs. Graceful shutdown is tested with a slow handler. Every documented limit is mapped to its test in [docs/API.md](docs/API.md#limits). The unit tests cover configuration (including that the token is never echoed), authentication, strict JSON handling, every option range, error mapping with `Retry-After`, the render deadline, panic recovery, and that logs never contain request content.

```
cmd/pressroom        entry point (wiring only)
internal/config      environment configuration, fails fast
internal/api         HTTP handlers and request validation
internal/middleware  authentication, request log, panic recovery
internal/render      the Renderer interface, the Chromium engine, a test double
internal/limits      concurrency limit with a bounded queue
Dockerfile           image: pinned headless-shell, pruned fonts, non-root
railway.toml         Railway build and health check
examples/client      Go client and contract test, standard library only
docs/DEPLOY.md       private deployment on Railway and troubleshooting
docker/fonts.conf    maps serif and sans-serif to the bundled fonts
docs/API.md          the contract
docs/DESIGN.md       decisions and measurements
```

## Roadmap

- [x] API, authentication, validation, errors, logs, CI
- [x] Chromium engine with scripts disabled and all requests blocked, plus integration tests
- [x] Concurrency limit with a bounded queue, graceful drain of in-flight renders, browser crash recovery
- [x] Docker image with fonts (Noto Serif, Noto Serif CJK, Noto Sans) and a non-root user; the sandbox cannot be on under Docker's default seccomp profile
- [x] Deployment notes for private networking, a sample client and a contract test (not yet run on Railway itself)
- [ ] First tagged release

## Licence

[MIT](LICENSE)
