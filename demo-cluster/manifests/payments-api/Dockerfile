FROM golang:1.27.1-alpine3.24 AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY main.go ./
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /app .

# Alpine rather than distroless: trigger-oomkill.sh runs the image's busybox
# wget through `kubectl exec`.
FROM alpine:3.24.2
RUN adduser -D -u 10001 app
COPY --from=builder /app /app
USER 10001
EXPOSE 8080
ENTRYPOINT ["/app"]
