# Multi-stage build for the watcher. Build context is this directory (the
# Go module root):
#
#   docker build -t k8s-watcher:dev k8s-watcher/
#
# The result is one statically linked binary on distroless static:nonroot,
# with the license texts in /licenses.

# Multi-arch index digest of golang:1.27.1.
FROM golang:1.27.1@sha256:e0174e51e81218523251d85d248a90d24c3d5e81543b4f07a5d66229397db190 AS builder

# Build with the image's Go only: fail instead of downloading another
# toolchain if go.mod asks for a newer one.
ENV GOTOOLCHAIN=local

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION=dev
ARG TARGETOS=linux
ARG TARGETARCH

RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build \
      -trimpath \
      -ldflags "-s -w -X main.version=${VERSION}" \
      -o /out/watcher \
      ./cmd/watcher

# Multi-arch index digest of static-debian13:nonroot.
FROM gcr.io/distroless/static-debian13:nonroot@sha256:e2e927ec666bae08560abb3c55d0659eceabb657f56b6782ab500a9fc7f555e3

COPY LICENSE-APACHE NOTICE /licenses/
COPY --from=builder /out/watcher /watcher

# EV lane on 8080, CM lane on 8081 (each serves /healthz, /readyz and
# /metrics).
EXPOSE 8080 8081

USER nonroot:nonroot

ENTRYPOINT ["/watcher"]
