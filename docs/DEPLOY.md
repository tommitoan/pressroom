# Deploying pressroom on Railway as a private service

pressroom is meant to be called by your own services, never by browsers. On Railway that means a service with **no public domain**, reachable over the project's private network and protected by a bearer token.

> **Status of these steps.** The image, the contract and the private-network behaviour were checked locally: the image ran on a Docker network with no published port, locked down (`--read-only`, `--cap-drop ALL`, `--security-opt no-new-privileges`), and the contract test passed from another container. The Railway-specific parts below (private hostname, variable references, memory size) follow Railway's documented behaviour and have **not** been run on Railway. Check each marked step the first time.

Commands that only use `curl`, `openssl` and Docker are the same on macOS, Ubuntu and Fedora. Railway itself is configured in its dashboard.

## 1. Create the service

1. In your Railway project, add a service from the GitHub repository `tommitoan/pressroom`, branch `main`.
2. Railway reads `railway.toml`: it builds the `Dockerfile` and checks `GET /ready` before it routes traffic. `/ready` starts the browser, so a deploy whose Chromium cannot start fails instead of going live.
3. Name the service `pressroom`. The name is part of its private address.

## 2. Variables

| Variable | Value | Why |
|---|---|---|
| `PRESSROOM_TOKEN` | output of `openssl rand -hex 32` | required; at least 16 characters; the service refuses to start without it |
| `PORT` | `8080` | fixes the port callers use on the private network |
| `RENDER_CONCURRENCY` | `2` (default) | renders at once; lower it to `1` if the service runs out of memory |
| `RENDER_QUEUE` | `4` (default) | renders that may wait; the next ones get `429` |
| `RENDER_TIMEOUT` | `20s` (default) | raise it (up to `2m`) only for very large documents |

`CHROMIUM_NO_SANDBOX=true` is already the image default. Leave it: the sandbox needs user namespaces, which Docker's default seccomp profile blocks and Railway does not let a service change. See the security model in the README.

Never put the token in a file, in the repository or in a chat message. Rotate it by changing the variable on both services and redeploying.

## 3. Keep it private

- Do **not** click "Generate Domain" and do not add a custom domain. If a public domain exists, remove it. Without one the service is only reachable from the project's private network.
- The token is the second lock, not the first: anyone who could reach the service and learn the token could render pages with your CPU.

## 4. Call it from another service

In the calling service (for example `bazica-web`) set:

| Variable | Value |
|---|---|
| `PRESSROOM_URL` | `http://pressroom.railway.internal:8080` `[unverified on Railway: the hostname is `<service name>.railway.internal`]` |
| `PRESSROOM_TOKEN` | `${{pressroom.PRESSROOM_TOKEN}}` `[unverified: Railway variable reference, so both services share one value]` |

Then copy [`examples/client/client.go`](../examples/client/client.go) into the caller (it only uses the Go standard library) and call:

```go
c := client.New(os.Getenv("PRESSROOM_URL"), os.Getenv("PRESSROOM_TOKEN"))
pdf, err := c.PDF(ctx, html, nil)
```

The client retries `429 busy` and `503 unavailable` after the `Retry-After` the service sends and never retries anything else.

## 5. Check it

From a shell inside the calling service, or any service on the same private network (replace the URL if your service name differs):

```sh
curl -s -o /dev/null -w '%{http_code}\n' http://pressroom.railway.internal:8080/ready   # 200
```

Then run the contract test against it. Copy [`examples/client/contract_test.go`](../examples/client/contract_test.go) next to `client.go` and run:

```sh
PRESSROOM_URL=http://pressroom.railway.internal:8080 PRESSROOM_TOKEN=<token> \
  go test -count=1 -v -run Contract
```

The same test runs against any local instance (`make docker-build`, `make docker-run`, then `PRESSROOM_URL=http://localhost:8080 make contract`).

Also check, once, from your own computer, that the service is **not** reachable from outside: its private hostname must not resolve and no public URL must exist.

## 6. Size and cost

Measured in the image on a 4 GB machine: about 90 MB after twenty sequential renders, a peak of 103 MiB with the default concurrency of 2 under a burst of twelve requests, 37 to 51 ms for a small three-page document. Heavy pages need more, so start with a plan that gives the service **at least 512 MB** and watch the memory graph during the first real exports. Raise the memory, or set `RENDER_CONCURRENCY=1`, if the service restarts. `[unverified on Railway]`

## Troubleshooting

| Symptom | Likely cause and what to do |
|---|---|
| Deploy fails its health check, or `/ready` is `503` | the browser did not start. Read the log for `browser start failed`. Check memory first, then that `CHROMIUM_NO_SANDBOX` is `true` |
| `401 unauthorized` | the caller's `PRESSROOM_TOKEN` differs from the service's |
| `429 busy` | more renders than `RENDER_CONCURRENCY` plus `RENDER_QUEUE`. The client retries; for sustained load raise both and the memory |
| `504 render_timeout` | the document is heavy. Simplify it (fewer pages, smaller images) or raise `RENDER_TIMEOUT` |
| `413 payload_too_large` | the request body is over 2 MiB. Inline fewer or smaller images; raise `MAX_BODY_BYTES` if needed |
| The service restarts under load | out of memory. Add memory or lower `RENDER_CONCURRENCY` |
| Han characters print as empty boxes | the page does not name a font the image has. Use `"Noto Serif", "Noto Serif CJK SC", serif` |
| Background colours or images are missing | send `"print_background": true` (the default) and set the colour on `body` or on an element, not only on `html` |
| A page background does not reach the page edge | a background on `body` fills the content area only. Use `@page { margin: 0 }` and pad the content |
| Text copied from the PDF looks slightly wrong | Chrome extracts some Han glyphs as Kangxi radicals and `Đ` as `Ð`; the PDF itself is correct |
