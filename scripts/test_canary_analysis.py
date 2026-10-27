"""Tests for scripts/canary-analysis.sh against a fake Prometheus API.

Run: python3 -m pytest scripts/ -q
"""
import json
import os
import subprocess
import threading
from http.server import BaseHTTPRequestHandler, HTTPServer
from pathlib import Path
from urllib.parse import parse_qs, urlparse

import pytest

SCRIPT = Path(__file__).with_name("canary-analysis.sh")


def make_server(metrics: dict):
    """metrics: {"err_canary", "err_stable", "p99_canary", "p99_stable", "requests"} -> value or None"""

    class Handler(BaseHTTPRequestHandler):
        def do_GET(self):  # noqa: N802
            q = parse_qs(urlparse(self.path).query)["query"][0]
            track = "canary" if 'track="canary"' in q else "stable"
            if "increase(" in q:
                key = "requests"
            elif "histogram_quantile" in q:
                key = f"p99_{track}"
            else:
                key = f"err_{track}"
            val = metrics.get(key)
            result = [] if val is None else [{"metric": {}, "value": [0, str(val)]}]
            body = json.dumps({"status": "success", "data": {"resultType": "vector", "result": result}})
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.end_headers()
            self.wfile.write(body.encode())

        def log_message(self, *args):
            pass

    srv = HTTPServer(("127.0.0.1", 0), Handler)
    threading.Thread(target=srv.serve_forever, daemon=True).start()
    return srv


def run(metrics: dict) -> subprocess.CompletedProcess:
    srv = make_server(metrics)
    try:
        env = dict(os.environ,
                   APP="orders",
                   PROM_URL=f"http://127.0.0.1:{srv.server_port}",
                   ANALYSIS_DURATION="2", INTERVAL="1")
        return subprocess.run(["bash", str(SCRIPT)], env=env, capture_output=True, text=True, timeout=30)
    finally:
        srv.shutdown()


HEALTHY = {"err_canary": 0.001, "err_stable": 0.001, "p99_canary": 0.08, "p99_stable": 0.07, "requests": 5000}


def test_healthy_canary_passes():
    r = run(HEALTHY)
    assert r.returncode == 0, r.stdout + r.stderr
    assert "PASS" in r.stdout


@pytest.mark.parametrize("override,reason", [
    ({"err_canary": 0.2}, "error ratio"),
    ({"p99_canary": 0.2}, "x 1.5"),          # 0.2 > 0.07 * 1.5
    ({"p99_canary": 0.9, "p99_stable": 0.8}, "absolute limit"),
    ({"requests": 10}, "no verdict"),
])
def test_bad_canary_fails(override, reason):
    r = run({**HEALTHY, **override})
    assert r.returncode == 1, r.stdout
    assert reason in r.stdout


def test_stable_noise_raises_error_budget_for_canary():
    # Stable itself has 3% errors (e.g. downstream outage): canary at 4% is not worse.
    r = run({**HEALTHY, "err_stable": 0.03, "err_canary": 0.04})
    assert r.returncode == 0, r.stdout


def test_no_stable_data_uses_absolute_threshold():
    r = run({**HEALTHY, "err_stable": None, "p99_stable": None, "err_canary": 0.005})
    assert r.returncode == 0, r.stdout
