# pressroom API (v1)

Contract for `POST /v1/pdf`. The service renders one self-contained HTML document to a PDF with headless Chromium. It is private: reachable only from other services on the same network, with a bearer token.

## Endpoints

| Method and path | Purpose |
|---|---|
| `POST /v1/pdf` | render HTML to PDF |
| `GET /health` | the process is up; always 200 while running; no authentication |
| `GET /ready` | the browser can start a render; 200 or 503; no authentication |

`/health` and `/ready` carry no data and are meant for the platform's checks.

## `POST /v1/pdf`

Headers: `Authorization: Bearer <token>`, `Content-Type: application/json`.

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

| Field | Type | Default | Rule |
|---|---|---|---|
| `html` | string | required | at most 2 MiB; a complete document |
| `options.paper` | string | `A4` | one of `A4`, `A3`, `Letter`, `Legal` |
| `options.landscape` | bool | false | |
| `options.margins_mm.*` | number | 15, 12, 18, 12 (top, right, bottom, left) | each 0 to 50 |
| `options.print_background` | bool | true | |
| `options.prefer_css_page_size` | bool | true | a CSS `@page` size and margins win over the options |
| `options.scale` | number | 1 | 0.5 to 2 |
| `options.header_html`, `footer_html` | string | none | at most 16 KiB each; Chromium header and footer templates (`pageNumber`, `totalPages`, `date`, `title` classes) |

Unknown fields, at any level, are rejected with `invalid_json`. Unset margins keep their own defaults.

Success: `200`, `Content-Type: application/pdf`, `Content-Length`, the PDF as the body. The service sets no file name; the caller adds `Content-Disposition`.

## Errors

JSON body `{ "error": "<message safe to show a developer>", "code": "<code>" }`. Messages never contain the HTML.

| Status | `code` | When |
|---|---|---|
| 400 | `invalid_json` | the body is not one JSON object, has unknown fields, wrong types or trailing data |
| 400 | `html_required`, `invalid_options` | `html` is missing or blank; an option is outside its range |
| 401 | `unauthorized` | missing or wrong token |
| 413 | `payload_too_large` | body over the limit (this also catches an oversized header or footer when the limit is below 16 KiB) |
| 429 | `busy` | the render queue is full; has `Retry-After` |
| 503 | `unavailable` | the browser cannot start or restarted mid-render; has `Retry-After` |
| 504 | `render_timeout` | the render exceeded its time limit |
| 500 | `render_failed` | any other render failure |
| 500 | `internal_error` | an unexpected fault in the service itself |
| 404 | `not_found` | no such endpoint |
| 405 | `method_not_allowed` | the endpoint exists but not for this method; has `Allow` |

## What the rendered page can and cannot do

- Scripts do not run.
- Every network request the page makes is intercepted and failed: stylesheets, images, fonts, frames, `fetch`. The service never loads a URL. `file:` is blocked by the browser itself.
- `data:` URIs work (inline images and SVG, measured in the spike), so logos and small graphics can be embedded.
- **Fonts must come from the image**, not from the web. The image provides Noto Serif (Latin and Vietnamese), Noto Serif CJK SC (Han characters) and Noto Sans as a fallback. A document should use the stack `"Noto Serif", "Noto Serif CJK SC", serif`.
- Each render uses a new page in a shared browser, and nothing is stored.

## Limits (starting values, tuned in Phase B4)

| Limit | Value |
|---|---|
| request body | 2 MiB |
| render time | 20 seconds |
| concurrent renders | 2 |
| queue behind them | 4 |
| header and footer | 16 KiB each |

Beyond the queue the service answers 429 quickly instead of waiting.

## Security model

- Network: no public domain; only other services on the private network call it.
- Authentication: one shared bearer token from `PRESSROOM_TOKEN`, compared in constant time. The service refuses to start without it.
- Privacy: request bodies are never logged or stored. Logs carry method, path, status, duration and sizes.
- Isolation: the browser runs as a non-root user; each render gets its own page and temporary profile, removed afterwards.

## Versioning

Paths carry the major version (`/v1`). Fields may be added without a new version; removing or changing a field needs `/v2`.
