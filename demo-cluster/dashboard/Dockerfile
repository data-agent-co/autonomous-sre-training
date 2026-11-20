# Multi-stage build for the demo dashboard. Build context is this directory
# (the Go module root):
#
#   docker build -t demo-dashboard:demo demo-cluster/dashboard/
#
# The result is one statically linked binary on distroless static:nonroot.
# The page (web/), with uPlot and its license, is embedded in the binary.

# Multi-arch index digest of golang:1.27.1.
FROM golang:1.27.1@sha256:e0174e51e81218523251d85d248a90d24c3d5e81543b4f07a5d66229397db190 AS builder

# Build with the image's Go only: fail instead of downloading another
# toolchain if go.mod asks for a newer one.
ENV GOTOOLCHAIN=local

WORKDIR /src

# The module uses the standard library only: there is no go.sum and nothing
# to download.
COPY go.mod main.go ./
COPY web ./web

ARG TARGETOS=linux
ARG TARGETARCH

RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build \
      -trimpath \
      -ldflags "-s -w" \
      -o /out/dashboard \
      .

# Multi-arch index digest of static-debian13:nonroot.
FROM gcr.io/distroless/static-debian13:nonroot@sha256:e2e927ec666bae08560abb3c55d0659eceabb657f56b6782ab500a9fc7f555e3

COPY --from=builder /out/dashboard /dashboard

EXPOSE 8080

# The distroless `nonroot` user, by number: with a name the kubelet cannot
# check runAsNonRoot (deployment.yaml).
USER 65532:65532

ENTRYPOINT ["/dashboard"]
