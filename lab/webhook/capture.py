#!/usr/bin/env python3
"""A capture receiver for trying webhooks in the lab. It is a test tool, not a service.

It listens on 127.0.0.1 only, accepts a POST on one secret path, answers 404 to everything else,
and appends what it received to received.jsonl. It runs nothing and forwards nothing. For each
message it records whether the bearer token and the forward-1keyes signature were valid; it does
not store either value. Because it is for looking at what arrives, it answers 200 even when the
token or signature is wrong; a real receiver must refuse those.

    python3 -I capture.py DIR [PORT]

DIR must hold these files (one line each, mode 600):
    path.secret   the last segment of the path:  /hook/<path.secret>
    bearer.secret the bearer token a ThousandEyes connector is configured to send
    sign.secret   the NOTIFY_SECRET given to `forward-1keyes changes --notify`
"""
import hashlib
import hmac
import http.server
import json
import os
import sys
import time

_LIMIT = 64 * 1024


def main():
    here = sys.argv[1]
    port = int(sys.argv[2]) if len(sys.argv) > 2 else 8644

    def secret(name):
        with open(os.path.join(here, name)) as handle:
            value = handle.read().strip()
        if len(value) < 16:
            raise SystemExit(name + " must hold at least 16 characters")
        return value

    path = "/hook/" + secret("path.secret")
    bearer = ("Bearer " + secret("bearer.secret")).encode()
    signing = secret("sign.secret").encode()
    out = os.path.join(here, "received.jsonl")
    os.umask(0o077)

    def check(got, want):
        if got is None:
            return "absent"
        return "valid" if hmac.compare_digest(got.encode(), want) else "INVALID"

    class Handler(http.server.BaseHTTPRequestHandler):
        timeout = 10
        server_version = "capture"
        sys_version = ""

        def answer(self, code):
            self.send_response(code)
            self.send_header("Content-Length", "0")
            self.end_headers()

        def do_POST(self):
            if not hmac.compare_digest(self.path.encode(), path.encode()):
                return self.answer(404)
            try:
                length = int(self.headers.get("Content-Length", "-1"))
            except ValueError:
                length = -1
            if length < 0 or length > _LIMIT:
                return self.answer(413)
            body = self.rfile.read(length)
            expected = "sha256=" + hmac.new(signing, body, hashlib.sha256).hexdigest()
            record = {
                "at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
                "bearer": check(self.headers.get("Authorization"), bearer),
                "signature": check(self.headers.get("X-1keyes-Signature"), expected.encode()),
                "content_type": self.headers.get("Content-Type", ""),
                "user_agent": self.headers.get("User-Agent", ""),
                "body": body.decode("utf-8", "replace"),
            }
            with open(out, "a") as handle:
                handle.write(json.dumps(record) + "\n")
            return self.answer(200)

        def do_GET(self):
            self.answer(404)

        do_HEAD = do_PUT = do_DELETE = do_PATCH = do_OPTIONS = do_GET

        def log_message(self, *args):
            pass

    print("listening on 127.0.0.1:%d; messages go to %s" % (port, out), flush=True)
    http.server.ThreadingHTTPServer(("127.0.0.1", port), Handler).serve_forever()


if __name__ == "__main__":
    main()
