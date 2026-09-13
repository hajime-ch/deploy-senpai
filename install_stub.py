"""Stands in for GitHub's release endpoints so install_test.sh runs offline.

Configured by env: TAG, ASSET (filename), CHECKSUMS (good|bad|missing),
BODY_VERSION (what the packaged fake binary reports).
"""
import http.server, io, json, os, tarfile, hashlib, sys

TAG = os.environ.get("TAG", "v0.9.9")
ASSET = os.environ["ASSET"]
CHECKSUMS = os.environ.get("CHECKSUMS", "good")
BODY_VERSION = os.environ.get("BODY_VERSION", "0.9.9")


def build_archive():
    script = f'#!/bin/sh\n[ "$1" = version ] && echo {BODY_VERSION}\n'.encode()
    buf = io.BytesIO()
    with tarfile.open(fileobj=buf, mode="w:gz") as tf:
        info = tarfile.TarInfo("deploy-senpai")
        info.size = len(script)
        info.mode = 0o755
        tf.addfile(info, io.BytesIO(script))
    return buf.getvalue()


ARCHIVE = build_archive()
DIGEST = hashlib.sha256(ARCHIVE).hexdigest()
# Only the "bad" mode corrupts the checksum; "missing" just withholds the file.
PUBLISHED = "d" * 64 if CHECKSUMS == "bad" else DIGEST


class H(http.server.BaseHTTPRequestHandler):
    def log_message(self, *a):
        pass

    def _send(self, code, body=b"", headers=()):
        self.send_response(code)
        for k, v in headers:
            self.send_header(k, v)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        if self.command != "HEAD":
            self.wfile.write(body)

    def do_HEAD(self):
        self.do_GET()

    def do_GET(self):
        p = self.path
        if p == "/releases/latest":
            return self._send(302, b"", [("Location", f"/releases/tag/{TAG}")])
        if p == f"/releases/tag/{TAG}":
            return self._send(200, b"release page")
        if p == f"/releases/download/{TAG}/checksums.txt":
            if CHECKSUMS == "missing":
                return self._send(404, b"not found")
            return self._send(200, f"{PUBLISHED}  {ASSET}\n".encode())
        if p == f"/releases/download/{TAG}/{ASSET}":
            return self._send(200, ARCHIVE)
        if p.startswith("/api/"):
            body = json.dumps({
                "tag_name": TAG,
                "assets": [{
                    "name": ASSET,
                    "browser_download_url": f"http://127.0.0.1:{PORT}/releases/download/{TAG}/{ASSET}",
                    "digest": "sha256:" + PUBLISHED,
                }],
            }).encode()
            return self._send(200, body, [("Content-Type", "application/json")])
        self._send(404, b"not found")


PORT = int(sys.argv[1])
http.server.HTTPServer(("127.0.0.1", PORT), H).serve_forever()
