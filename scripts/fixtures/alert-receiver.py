"""T004/REQ-020：仅隔离测试使用的合成Webhook接收器，不部署到生产。"""
from http.server import BaseHTTPRequestHandler, HTTPServer
import json
from pathlib import Path
import tempfile


class Handler(BaseHTTPRequestHandler):
    def do_POST(self):
        size = int(self.headers.get("Content-Length", "0"))
        if self.path != "/notify" or not 0 < size <= 1048576:
            self.send_error(400)
            return
        payload = json.loads(self.rfile.read(size))
        # 只持久化本次合成告警JSON，不复制HTTP头/鉴权信息或输出通知内容。
        with tempfile.NamedTemporaryFile(mode="w", dir="/data/notifications", delete=False) as output:
            json.dump(payload, output)
        self.send_response(200)
        self.send_header("Content-Length", "2")
        self.end_headers()
        self.wfile.write(b"{}")

    def log_message(self, *_):
        pass


Path("/data/notifications").mkdir(exist_ok=True)
HTTPServer(("0.0.0.0", 8080), Handler).serve_forever()
