#!/usr/bin/env python3
"""T004/REQ-018、020：真实验证启停入口、默认管理/监控、重启与数据卷保留。"""
import base64
import hashlib
import json
import os
from pathlib import Path
import secrets
import socket
import subprocess
import tempfile
import time
import urllib.parse
import urllib.request

ROOT = Path(__file__).resolve().parents[1]


def main():
    project = "liyuanci-dev-ops-" + secrets.token_hex(5)
    monitor = project + "-observability"
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    # 固定应用/管理端口若被占用则停止本次验收，不停止既有容器。
    for port in (18080, 15050, 15540, 18081):
        with socket.socket() as listener:
            listener.bind(("127.0.0.1", port))
    with socket.socket() as listener:
        listener.bind(("127.0.0.1", 0))
        grafana_port = listener.getsockname()[1]

    def run(command):
        result = subprocess.run(command, cwd=ROOT, text=True, capture_output=True, timeout=1200)
        if result.returncode:
            # 生命周期入口只输出安全消息/结构化完成记录，不转储环境或原始Docker输出。
            if command[0].endswith(".sh"):
                print(result.stdout, flush=True)
                print(result.stderr, flush=True)
            raise RuntimeError("生命周期验收操作失败，project=" + project)
        return result.stdout

    def wait(fn, description):
        deadline = time.monotonic() + 180
        while time.monotonic() < deadline:
            try:
                if fn():
                    print("PASS " + description, flush=True)
                    return
            except (OSError, ValueError, KeyError):
                pass
            time.sleep(2)
        raise RuntimeError("timeout: " + description)

    def http(url, headers=None):
        with opener.open(urllib.request.Request(url, headers=headers or {}), timeout=10) as response:
            return response.status, response.headers, response.read()

    def ids(kind, name, all_containers=False):
        command = ["docker", kind, "ls"] if kind != "container" else ["docker", "ps"] + (["-a"] if all_containers else [])
        return run(command + ["--filter", "label=com.docker.compose.project=" + name,
                              "--format", "{{.Name}}" if kind == "volume" else "{{.ID}}"]).split()

    with tempfile.TemporaryDirectory(prefix=project) as directory:
        config = Path(directory) / "config"
        entry = config / "operations.env"
        start = [str(ROOT / "scripts/start.sh"), "dev", "--project", project, "--env-file", str(entry)]
        stop = [str(ROOT / "scripts/stop.sh"), "dev", "--project", project]
        try:
            init = [str(ROOT / "scripts/init-dev.sh"), "--project", project, "--config-dir", str(config)]
            run(init)
            before = {str(path): hashlib.sha256(path.read_bytes()).hexdigest() for path in config.rglob("*") if path.is_file()}
            run(init)
            assert before == {str(path): hashlib.sha256(path.read_bytes()).hexdigest() for path in config.rglob("*") if path.is_file()}
            with entry.open("a") as output:
                output.write("LIYUANCI_GRAFANA_PORT=" + str(grafana_port) + "\n")
            print("PASS 开发凭据初始化幂等且不覆盖已有文件", flush=True)
            run(start + ["--bootstrap-probe", "--probe"])
            print("PASS 默认全栈启动、显式迁移/主题及真实独立worker往返", flush=True)
            for url in ("http://127.0.0.1:18080/healthz", "http://127.0.0.1:15050/misc/ping",
                        "http://127.0.0.1:15540/api/health/", "http://127.0.0.1:18081/",
                        "http://127.0.0.1:" + str(grafana_port) + "/api/health"):
                wait(lambda: http(url)[0] == 200, "HTTP " + url)
            _, headers, _ = http("http://127.0.0.1:18080/healthz")
            request_id = headers["X-Request-ID"]
            password = (config / "observability/grafana-password").read_text().strip()
            authorization = {"Authorization": "Basic " + base64.b64encode(("operator:" + password).encode()).decode()}
            expression = '{environment="development",service="api"} | json | request_id="' + request_id + '"'
            url = "http://127.0.0.1:" + str(grafana_port) + "/api/datasources/proxy/uid/liyuanci-loki/loki/api/v1/query_range?"
            url += urllib.parse.urlencode({"query": expression})
            wait(lambda: bool(json.loads(http(url, authorization)[2])["data"]["result"]), "真实API请求完成日志可查询")
            run(stop)
            assert not ids("container", project) and not ids("container", monitor)
            assert ids("container", project, True) and ids("container", monitor, True)
            assert ids("volume", project) and ids("volume", monitor)
            print("PASS 默认停止全部服务，容器/网络/卷保留", flush=True)
            restarted = run(start + ["--no-build", "--probe"])
            assert "显式迁移探针表" not in restarted and "显式创建探针主题" not in restarted
            print("PASS 复用原凭据/数据重启，无隐式迁移，探针再次往返", flush=True)
            run(stop + ["--remove"])
            for name in (project, monitor):
                assert not ids("container", name, True) and not ids("network", name)
                assert ids("volume", name)
            print("PASS 显式移除仅清理本项目容器/网络，数据卷保留；project=" + project, flush=True)
        finally:
            # 即使启动中途失败也按同一归属清理；stop不依赖已加载的凭据。
            run(stop + ["--remove"])


if __name__ == "__main__":
    main()
