# syntax=docker/dockerfile:1

FROM golang:1.26-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/pressroom ./cmd/pressroom

# The browser comes from the chromedp headless-shell image, pinned so a rebuild
# does not change the rendering engine by surprise.
FROM chromedp/headless-shell:151.0.7922.109

# Fonts are part of the contract: a rendered page cannot download any, and the
# stock image has none, so Han characters would print as empty boxes. Only the
# families the documented font stack needs are kept; the full packages are
# about 120 MB larger. Installing and pruning happen in one layer so the
# unused files never reach the image.
RUN apt-get update \
 && apt-get install -y --no-install-recommends fontconfig fonts-noto-core fonts-noto-cjk \
 && find /usr/share/fonts -type f \
      ! -name 'NotoSerif-*.ttf' \
      ! -name 'NotoSans-*.ttf' \
      ! -name 'NotoSerifCJK-Regular.ttc' \
      ! -name 'NotoSerifCJK-Bold.ttc' \
      -delete \
 && rm -rf /var/lib/apt/lists/* \
 && useradd --system --uid 10001 --no-create-home --shell /usr/sbin/nologin pressroom

COPY docker/fonts.conf /etc/fonts/conf.d/60-pressroom.conf
RUN fc-cache -f

COPY --from=build /out/pressroom /usr/local/bin/pressroom

# The Chromium sandbox needs user namespaces, which Docker's default seccomp
# profile blocks (hosted platforms do not let a service change it). Without
# this setting the browser cannot start at all. The compensating controls are
# the non-root user, scripts disabled and every page request refused; hosts
# that allow user namespaces can set CHROMIUM_NO_SANDBOX=false.
ENV PORT=8080 \
    CHROME_PATH=/headless-shell/headless-shell \
    CHROMIUM_NO_SANDBOX=true \
    HOME=/tmp
USER 10001:10001
EXPOSE 8080
# The base image starts a debugging proxy; this image runs the service instead.
ENTRYPOINT ["/usr/local/bin/pressroom"]
