#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
"""A loopback stand-in for a local model server (Ollama), for the demo estate.

It answers the three read calls the engine makes of a local endpoint (`GET /api/version`,
`GET /api/tags` and `GET /v1/models`) with the models the recorded sessions use. It serves no
completions: the demo tools answer from recorded transcripts and never call a model.

usage: demo-ollama.py [port]      (default 11434, 127.0.0.1 only)
"""
import json
import os
import sys
from http.server import BaseHTTPRequestHandler, HTTPServer

HERE = os.path.dirname(os.path.realpath(__file__))


def local_models():
    with open(os.path.join(HERE, "conversations.json"), encoding="utf8") as fh:
        sessions = json.load(fh)["sessions"]
    return sorted({s["model"] for s in sessions if s.get("local")})


class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        models = local_models()
        if self.path == "/api/version":
            body = {"version": "0.12.0"}
        elif self.path == "/api/tags":
            body = {"models": [{"name": m, "model": m, "size": 18_500_000_000,
                                "details": {"family": "qwen3", "parameter_size": "30B", "quantization_level": "Q4_K_M"}}
                               for m in models]}
        elif self.path == "/v1/models":
            body = {"object": "list", "data": [{"id": m, "object": "model", "owned_by": "library"} for m in models]}
        else:
            self.send_error(404)
            return
        raw = json.dumps(body).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)

    def log_message(self, *_):
        pass


if __name__ == "__main__":
    port = int(sys.argv[1]) if len(sys.argv) > 1 else 11434
    server = HTTPServer(("127.0.0.1", port), Handler)
    # Written only after the bind succeeded: a caller that reads this line knows the port is this process's,
    # not another server's that happened to answer first.
    print("demo-ollama: listening on 127.0.0.1:%d" % port, flush=True)
    server.serve_forever()
