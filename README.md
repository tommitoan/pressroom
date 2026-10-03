# pressroom

[![CI](https://github.com/tommitoan/pressroom/actions/workflows/ci.yml/badge.svg)](https://github.com/tommitoan/pressroom/actions/workflows/ci.yml)
[![Go](https://img.shields.io/badge/go-1.22%2B-00ADD8?logo=go&logoColor=white)](https://go.dev/)
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

> **Status: early development.** The HTTP API, authentication, validation and limits are done and tested. The Chromium rendering engine and the Docker image are the next milestones (see the [roadmap](#roadmap)); until then `POST /v1/pdf` answers `503 unavailable` and `GET /ready` answers `503`, on purpose, so the service never pretends to render.

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
| Next | Chromium engine: scripts disabled, every request intercepted and failed, one isolated page per render |
| Next | Concurrency limit with a bounded queue (fast `429` instead of waiting) |
| Next | Docker image with Han and Vietnamese-capable fonts, non-root |

## How it works

```
 caller (private network)
    |  POST /v1/pdf   { "html": "...", "options": { ... } }
    |  Authorization: Bearer <token>
    v
+--------------------------- pressroom ----------------------------+
|  request log -> recover -> bearer auth -> validate -> Renderer   |
|                                                         |        |
|                                       (next) Chromium engine     |
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

### A Go client in a few lines

```go
func pdf(ctx context.Context, base, token, html string) ([]byte, error) {
    body, _ := json.Marshal(map[string]any{"html": html})
    req, _ := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/pdf", bytes.NewReader(body))
    req.Header.Set("Authorization", "Bearer "+token)
    req.Header.Set("Content-Type", "application/json")
    resp, err := http.DefaultClient.Do(req)
    if err != nil {
        return nil, err
    }
    defer resp.Body.Close()
    if resp.StatusCode != http.StatusOK {
        return nil, fmt.Errorf("pressroom: status %d", resp.StatusCode)
    }
    return io.ReadAll(resp.Body)
}
```

## Security model

pressroom renders HTML written by someone else, so the page is treated as untrusted.

**Enforced by the code today**

- **Private by design.** Run it on a private network with no public domain. One shared bearer token protects the render endpoint, compared in constant time; it must be at least 16 characters and the service refuses to start without it.
- **Nothing stored, nothing logged.** Logs hold method, path, status, duration and sizes; never bodies, queries or tokens. Tests assert this.
- **Errors say nothing about the input.** Messages are fixed text.
- **Bounded input.** Strict JSON, unknown fields rejected, body size limit, option ranges, a deadline per render.

**Provided by the Chromium engine milestone** (behaviour verified in a spike, see [docs/DESIGN.md](docs/DESIGN.md))

- **No scripts, no network.** JavaScript is disabled and every request the page makes (stylesheets, images, fonts, frames, `fetch`) is intercepted and failed. The service never loads a URL. `data:` URIs work, so logos and small graphics can be inlined.
- **Contained.** Each render gets its own page in a shared browser, and the browser runs as a non-root user in the container.

## Configuration

| Variable | Default | Meaning |
|---|---|---|
| `PRESSROOM_TOKEN` | required | shared secret, at least 16 characters |
| `PORT` | `8080` | listen port |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `MAX_BODY_BYTES` | `2097152` | request body limit (1024 to 67108864) |
| `RENDER_TIMEOUT` | `20s` | per-render time limit (1s to 2m) |

Copy `.env.example` for a starting point and never commit a real token. Generate one with `openssl rand -hex 32`.

## Run it

Needs Go 1.22 or newer. The steps are the same on macOS, Ubuntu and Fedora; only installing Go differs.

```sh
export PRESSROOM_TOKEN="$(openssl rand -hex 32)"
make run                 # go run ./cmd/pressroom
curl -s localhost:8080/health
```

## Development

```sh
make check               # gofmt, go vet, tests with the race detector, build
make test                # tests only
```

The tests use a fake renderer, so they need no browser. They cover configuration (including that the token is never echoed), authentication, strict JSON handling, every option range, error mapping with `Retry-After`, the render deadline, panic recovery, and that logs never contain request content.

```
cmd/pressroom        entry point (wiring only)
internal/config      environment configuration, fails fast
internal/api         HTTP handlers and request validation
internal/middleware  authentication, request log, panic recovery
internal/render      the Renderer interface, an unavailable renderer, a test double
docs/API.md          the contract
docs/DESIGN.md       decisions and measurements
```

## Roadmap

- [x] API, authentication, validation, errors, logs, CI
- [ ] Chromium engine with scripts disabled and all requests blocked, plus integration tests
- [ ] Concurrency limit with a bounded queue, graceful drain of in-flight renders, browser crash recovery
- [ ] Docker image with fonts (Noto Serif, Noto Serif CJK, Noto Sans), non-root, sandbox on
- [ ] Deployment notes for private networking, a sample client and a contract test
- [ ] First tagged release

## Licence

[MIT](LICENSE)
