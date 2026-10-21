# syntax=docker/dockerfile:1
# The image runs `uneguictl watch`, the Telegram bot; see k8s/README.md.

# ---- build ----------------------------------------------------------------
FROM golang:1.27-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
ARG VERSION=dev
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath \
      -ldflags="-s -w -X github.com/orshih6/uneguictl/internal/cli.version=${VERSION}" \
      -o /out/uneguictl ./cmd/uneguictl

# ---- runtime --------------------------------------------------------------
# static carries the CA bundle both unegui.mn and Telegram need, and nothing else.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/uneguictl /usr/local/bin/uneguictl
WORKDIR /data
ENTRYPOINT ["/usr/local/bin/uneguictl"]
CMD ["watch", "--state", "/data/watch.json"]
