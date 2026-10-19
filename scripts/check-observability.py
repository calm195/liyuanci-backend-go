#!/usr/bin/env python3
"""T004/REQ-018、020：隔离验证真实Docker采集、查询、重启和敏感日志阻断。"""
import base64
import json
import os
from pathlib import Path
import secrets
import subprocess
import tempfile
import time
import urllib.parse
import urllib.request


def main():
    project = "liyuanci-logs-" + secrets.token_hex(5)
    marker = secrets.token_hex(12)
    password = secrets.token_hex(24)
    port = os.environ.get("LIYUANCI_CHECK_GRAFANA_PORT", "13999")
    env = os.environ.copy()
    fixtures = []
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    auth = base64.b64encode(("operator:" + password).encode()).decode()
    with tempfile.TemporaryDirectory(prefix=project) as directory:
        secret = Path(directory) / "grafana-password"
        secret.write_text(password)
        secret.chmod(0o644)  # 父目录0700；容器中的Grafana非root用户需读取文件。
        env.update(LIYUANCI_GRAFANA_PASSWORD_FILE=str(secret), LIYUANCI_GRAFANA_PORT=port,
                   LIYUANCI_LOG_PROJECT=project, LIYUANCI_LOG_ENVIRONMENT="verification",
                   LIYUANCI_LOG_HOST="isolated", LIYUANCI_DOCKER_SOCKET=env.get(
                       "LIYUANCI_DOCKER_SOCKET", "/var/run/docker.sock"))
        compose = ["docker", "compose", "-p", project, "-f", "compose.observability.yaml"]

        def run(args):
            result = subprocess.run(args, env=env, capture_output=True,
                                    text=True, timeout=600)
            if result.returncode:
                # 仅测试凭据可替换；不转储环境或Compose展开配置。
                print(result.stderr.replace(password, "[redacted]"), flush=True)
                raise RuntimeError("Docker操作失败，exit=" + str(result.returncode))
            return result.stdout

        def api(path):
            request = urllib.request.Request("http://127.0.0.1:" + port + path,
                                             headers={"Authorization": "Basic " + auth})
            with opener.open(request, timeout=10) as response:
                return json.load(response)

        def wait(fn, description):
            deadline = time.monotonic() + 150
            while time.monotonic() < deadline:
                try:
                    if fn():
                        print("PASS " + description, flush=True)
                        return
                except (OSError, ValueError, KeyError):
                    pass
                time.sleep(2)
            raise RuntimeError("timeout: " + description)

        def query(expression):
            path = "/api/datasources/proxy/uid/liyuanci-loki/loki/api/v1/query_range?"
            return api(path + urllib.parse.urlencode({"query": expression, "limit": 1000}))

        def fixture(target, service, line):
            name = project + "-" + service
            fixtures.append(name)
            # 仅打印合成日志，不触达现有业务容器；持续输出用于重启后的重新发现。
            run(["docker", "run", "-d", "--name", name, "--label",
                 "com.docker.compose.project=" + target, "--label",
                 "com.docker.compose.service=" + service,
                 "alpine:3.22.6@sha256:5291449c3df73caf6ed85e649dec1b9e818b39a5d8c871e97afc13e9cd5e8fa8",
                 "sh", "-c", 'while true; do printf "%s\\n" "$1"; sleep 2; done', "sh", line])

        try:
            run(compose + ["config", "--quiet"])
            run(compose + ["up", "-d"])
            wait(lambda: api("/api/health")["database"] == "ok", "Grafana健康")
            fixture(project, "api", json.dumps({"msg": "http request completed", "status": 500,
                    "request_id": marker, "level": "info", "duration": 0.01}))
            fixture(project, "blocked", json.dumps({"password": "synthetic-" + marker}))
            fixture(project + "-other", "foreign", marker)
            expression = '{environment="verification",service="api"} | json | request_id="' + marker + '"'
            wait(lambda: bool(query(expression)["data"]["result"]), "Docker→Alloy→Loki→Grafana真实查询")
            result = query('{environment="verification",service="api"} |= "' + marker + '"')["data"]["result"]
            assert set(result[0]["stream"]) == {"service", "environment", "host"}
            assert not query('{service="blocked"}')["data"]["result"]
            assert not query('{service="foreign"}')["data"]["result"]
            print("PASS 敏感日志阻断、项目隔离、低基数标签", flush=True)
            network = project + "_docker-logs"
            alpine = "alpine:3.22.6@sha256:5291449c3df73caf6ed85e649dec1b9e818b39a5d8c871e97afc13e9cd5e8fa8"
            for path in ["/images/json", "/containers/abc/archive"]:
                response = subprocess.run(["docker", "run", "--rm", "--network", network,
                    alpine, "wget", "-S", "-O", "-", "http://docker-log-proxy:2375" + path],
                    capture_output=True, text=True, timeout=30)
                assert "403" in response.stderr, "Docker代理未拒绝越权读取"
            response = subprocess.run(["docker", "run", "--rm", "--network", network,
                alpine, "wget", "-S", "-O", "-", "--post-data=", "http://docker-log-proxy:2375/_ping"],
                capture_output=True, text=True, timeout=30)
            assert "403" in response.stderr, "Docker代理未拒绝写入"
            print("PASS Docker代理拒绝archive、镜像读取及POST", flush=True)
            restarted_at = time.time_ns()
            run(compose + ["restart", "alloy", "loki"])
            wait(lambda: bool(query(expression)["data"]["result"]), "Alloy/Loki重启后可查询")
            wait(lambda: any(int(stamp) > restarted_at for stream in query(expression)["data"]["result"]
                             for stamp, _ in stream["values"]), "重启后继续采集新日志")
            assert api("/api/dashboards/uid/liyuanci-logs")["dashboard"]["uid"] == "liyuanci-logs"
            print("PASS 固定数据源与预置看板；project=" + project, flush=True)
        finally:
            for name in fixtures:
                subprocess.run(["docker", "rm", "-f", name], capture_output=True, timeout=30)
            subprocess.run(compose + ["down"], env=env, check=True, timeout=120)
            # 保留本次专用卷供排障；不删除业务卷，不输出临时凭据。


if __name__ == "__main__":
    main()
