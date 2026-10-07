#!/usr/bin/env python3
"""recommend-svc — the slow-leak victim's app container.

A plain HTTP service with Prometheus instrumentation and no fault code: the
leak lives in the leak-fixture sidecar (leak.py), which
scripts/trigger-leak.sh and scripts/trigger-slowleak.sh turn on and off.

Serves /healthz, and Prometheus metrics on /metrics (same :8080 port).
"""

import os
import time
from http.server import BaseHTTPRequestHandler, HTTPServer
from urllib.parse import urlparse

from prometheus_client import (
    CONTENT_TYPE_LATEST,
    Counter,
    Histogram,
    generate_latest,
)

PORT = int(os.environ.get("PORT", "8080"))

REQUESTS = Counter(
    "recommend_svc_http_requests_total",
    "HTTP requests total",
    ["method", "path", "status"],
)
DURATION = Histogram(
    "recommend_svc_http_request_duration_seconds",
    "HTTP request duration in seconds",
    ["method", "path"],
    buckets=(0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10),
)


class Handler(BaseHTTPRequestHandler):
    def _send(self, status, body=b"ok", content_type="text/plain"):
        self.send_response(status)
        self.send_header("Content-Type", content_type)
        self.end_headers()
        self.wfile.write(body if isinstance(body, bytes) else body.encode())

    def do_GET(self):
        path = urlparse(self.path).path
        start = time.time()
        status = 200
        try:
            if path == "/healthz":
                self._send(200, "ok")
            elif path == "/metrics":
                self._send(200, generate_latest(), CONTENT_TYPE_LATEST)
            else:
                status = 404
                self._send(404, "not found")
        except Exception:
            status = 500
            raise
        finally:
            REQUESTS.labels(method="GET", path=path, status=str(status)).inc()
            DURATION.labels(method="GET", path=path).observe(time.time() - start)

    def log_message(self, fmt, *args):
        return


def main():
    print(
        f"recommend-svc listening on :{PORT}  (/metrics on same port)",
        flush=True,
    )
    HTTPServer(("", PORT), Handler).serve_forever()


if __name__ == "__main__":
    main()
