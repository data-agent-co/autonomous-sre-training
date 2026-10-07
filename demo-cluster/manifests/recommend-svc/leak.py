#!/usr/bin/env python3
"""leak.py — fault fixture for the recommend-svc demo pod.

Runs as a SIDECAR container in the recommend-svc pod (not inside the
app code). Consumes memory at a rate controlled by the LEAK_RATE_MB_PER_MIN
env var (0 or unset = do nothing, just idle).

Design principle (fault-as-fixture):
  The application under test (main.py) contains ZERO fault-injection code.
  Faults live in this separate script, deployed alongside as a sidecar
  container. The container's memory usage shows up in cAdvisor as
  `container_memory_working_set_bytes{container="leak-fixture"}`, and
  when the fixture outgrows its container's memory limit the kernel
  OOMKills it and the kubelet restarts it — exactly like a real leak in one
  of the pod's containers.

  This is the usual chaos-engineering pattern: fault injection is a
  separate concern from the code being tested, applied via external
  YAML rather than baked into the app.

Behaviour:
  - LEAK_RATE_MB_PER_MIN=0 (default): idle. Sleeps forever. Container
    memory stays at Python-interpreter baseline (~15Mi).
  - LEAK_RATE_MB_PER_MIN=N (positive): allocate N MiB per minute of
    committed working-set memory. Writes one byte per 4KiB page so
    the kernel actually commits physical pages (not just VM address
    space). Buffer is held in a global list so it survives GC.

Signal handling:
  - SIGTERM: exit cleanly. Kubernetes will restart us; on restart we
    re-read LEAK_RATE_MB_PER_MIN, so `kubectl set env` toggles work
    across restarts automatically.
"""

import os
import signal
import sys
import time


def _read_rate() -> int:
    """Read LEAK_RATE_MB_PER_MIN from env; return 0 if unset/invalid."""
    try:
        rate = int(os.environ.get("LEAK_RATE_MB_PER_MIN", "0"))
        return max(0, rate)
    except ValueError:
        return 0


def _die(signum, _frame):
    print(f"leak-fixture: caught signal {signum}, exiting", flush=True)
    sys.exit(0)


def main():
    signal.signal(signal.SIGTERM, _die)
    signal.signal(signal.SIGINT, _die)

    rate = _read_rate()
    print(
        f"leak-fixture: LEAK_RATE_MB_PER_MIN={rate}  "
        f"({'idle' if rate == 0 else f'will consume {rate} MiB/min'})",
        flush=True,
    )

    if rate == 0:
        # No leak configured — idle forever. Container consumes minimal
        # memory (Python interpreter baseline).
        while True:
            time.sleep(3600)

    # Each tick allocates 1 MiB chunks until the total reaches rate x elapsed
    # time, so any rate holds. (One chunk per tick of at least 1s would cap
    # the leak at 60 MiB/min.)
    tick = min(1.0, 60.0 / rate)
    start = time.monotonic()
    buf = []  # keeps allocations reachable (no GC)
    while True:
        due = int(rate * (time.monotonic() - start) / 60.0) + 1
        while len(buf) < due:
            chunk = bytearray(1024 * 1024)  # 1 MiB
            for i in range(0, len(chunk), 4096):
                chunk[i] = 1  # force page commit
            buf.append(chunk)
            if len(buf) % 10 == 0:
                print(f"leak-fixture: {len(buf)} MiB allocated", flush=True)
        time.sleep(tick)


if __name__ == "__main__":
    main()
