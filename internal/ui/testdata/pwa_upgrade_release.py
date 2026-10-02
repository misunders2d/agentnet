#!/usr/bin/python3
"""Private loopback TLS release stand-in; serves one real fixture binary."""
import hashlib
import http.server
import json
import pathlib
import ssl
import sys

port, cert, key, asset, log = sys.argv[1:]
binary = pathlib.Path(asset).read_bytes()
asset_name = "agentnet-linux-amd64"
checksum = hashlib.sha256(binary).hexdigest()


class Release(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        with open(log, "a") as out:
            out.write(json.dumps({"path": self.path, "tls": True}) + "\n")
        prefix = "/releases/download/v9.9.9/"
        if self.path == prefix + "SHA256SUMS":
            body = (checksum + "  " + asset_name + "\n").encode()
        elif self.path == prefix + asset_name:
            body = binary
        else:
            self.send_error(404)
            return
        self.send_response(200)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *_):
        pass


server = http.server.ThreadingHTTPServer(("127.0.0.1", int(port)), Release)
context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
context.load_cert_chain(cert, key)
server.socket = context.wrap_socket(server.socket, server_side=True)
server.serve_forever()
