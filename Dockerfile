FROM --platform=$BUILDPLATFORM golang:1.27.4-bookworm@sha256:4fa7c968ba93f1635670522dbb01a2c78150443e1385746924566079f36570ea AS scout-build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY agent ./agent
COPY cmd ./cmd
COPY internal ./internal
COPY middleware ./middleware
COPY browse.go context.go engine.go errors.go group.go keypress.go lifecycle.go middleware.go options.go page.go page_fetch.go recorder.go selection.go selection_all.go table.go urlvalidator.go util.go ./

ARG TARGETOS
ARG TARGETARCH
ARG SCOUT_VERSION=dev
ARG SCOUT_COMMIT=none
RUN CGO_ENABLED=0 GOOS="$TARGETOS" GOARCH="$TARGETARCH" go build \
    -trimpath \
    -ldflags "-s -w -X main.version=$SCOUT_VERSION -X main.commit=$SCOUT_COMMIT" \
    -o /scout ./cmd/scout

FROM chromedp/headless-shell:stable@sha256:830fa77e0602cb42e5cb2cbd9237ba27b649ad69d5fb82b54713aac8b7ab4f29

RUN apt-get update \
    && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends zlib1g=1:1.3.dfsg+really1.3.1-1+deb13u1 \
    && rm -rf /var/lib/apt/lists/*

LABEL org.opencontainers.image.source="https://github.com/klarlabs-studio/scout"
LABEL org.opencontainers.image.description="AI-powered browser automation MCP server"
LABEL org.opencontainers.image.licenses="MIT"
LABEL io.modelcontextprotocol.server.name="io.github.klarlabs-studio/scout"

# The image runtime blocks Chromium's user-namespace sandbox. Keep Scout and
# Chromium unprivileged, and disable Chromium's sandbox only in this container.
RUN groupadd --system scout \
    && useradd --system --gid scout --home-dir /home/scout --create-home --shell /usr/sbin/nologin scout \
    && mkdir -p /home/scout/.cache \
    && chown -R scout:scout /home/scout

ENV HOME=/home/scout
ENV SCOUT_CHROME_NO_SANDBOX=1

COPY --from=scout-build --chown=scout:scout /scout /usr/local/bin/scout
USER scout

ENTRYPOINT ["scout"]
CMD ["mcp", "serve"]
