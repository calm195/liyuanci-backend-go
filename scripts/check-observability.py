#!/usr/bin/env python3
"""T004/REQ-018、020：隔离验证真实Docker采集、查询、重启和敏感日志阻断。"""
import argparse
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
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--alerts", action="store_true", help="增加本地合成Webhook触发/恢复测试")
    options = parser.parse_args()
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
        if options.alerts:
            compose += ["-f", "compose.alerting.yaml"]

        def run(args):
            result = subprocess.run(args, env=env, capture_output=True,
                                    text=True, timeout=600)
            if result.returncode:
                # 仅测试凭据可替换；不转储环境或Compose展开配置。
                print(result.stderr.replace(password, "[redacted]"), flush=True)
                raise RuntimeError("Docker操作失败，exit=" + str(result.returncode))
            return result.stdout

        def api(path, body=None, method=None):
            request = urllib.request.Request("http://127.0.0.1:" + port + path,
                data=json.dumps(body).encode() if body is not None else None,
                method=method,
                headers={"Authorization": "Basic " + auth, "Content-Type": "application/json"})
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
            name = project + "-" + service + "-" + secrets.token_hex(3)
            fixtures.append(name)
            # 仅打印合成日志，不触达现有业务容器；持续输出用于重启后的重新发现。
            run(["docker", "run", "-d", "--name", name, "--label",
                 "com.docker.compose.project=" + target, "--label",
                 "com.docker.compose.service=" + service,
                 "alpine:3.22.6@sha256:5291449c3df73caf6ed85e649dec1b9e818b39a5d8c871e97afc13e9cd5e8fa8",
                 "sh", "-c", 'while true; do printf "%s\\n" "$1"; sleep 2; done', "sh", line])
            return name

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
            # JSON查询临时提取的字段也会出现在结果标签中；索引边界必须用原文查询核验。
            result = query('{environment="verification",service="api"} |= "' + marker + '"')["data"]["result"]
            assert set(result[0]["stream"]) == {"service", "environment", "host"}, sorted(result[0]["stream"])
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
            if options.alerts:
                def rules_healthy():
                    groups = api("/api/prometheus/grafana/api/v1/rules")["data"]["groups"]
                    rules = {rule["name"]: rule for group in groups for rule in group["rules"]}
                    expected = {"T004 API panic", "T004 API 5xx ratio", "T004 worker failures",
                                "T004 api heartbeat missing", "T004 worker heartbeat missing"}
                    return expected <= rules.keys() and all(rules[name].get("health") == "ok" for name in expected)

                wait(rules_healthy, "五条日志规则真实求值无数据源或表达式错误")
                # T004/REQ-020：只向隔离网络中的合成接收器发消息，不联系外部人员。
                sink = Path(directory) / "sink"
                sink.mkdir()
                notifications = sink / "notifications"
                notifications.mkdir()
                name = project + "-notify"
                fixtures.append(name)
                run(["docker", "run", "-d", "--name", name, "--network", project + "_telemetry",
                     "--user", str(os.getuid()) + ":" + str(os.getgid()),
                     "--network-alias", "notify", "-v", str(sink) + ":/data",
                     "-v", str(Path("scripts/fixtures/alert-receiver.py").resolve()) + ":/receiver.py:ro",
                     "python:3.13.7-alpine@sha256:9ba6d8cbebf0fb6546ae71f2a1c14f6ffd2fdab83af7fa5669734ef30ad48844",
                     "python", "/receiver.py"])
                api("/api/v1/provisioning/contact-points", {
                    "uid": "isolated-webhook", "name": "isolated-webhook", "type": "webhook",
                    "settings": {"url": "http://notify:8080/notify", "httpMethod": "POST"},
                    "disableResolveMessage": False}, "POST")
                api("/api/v1/provisioning/policies", {
                    "receiver": "isolated-webhook", "group_by": ["alertname"],
                    "group_wait": "0s", "group_interval": "10s", "repeat_interval": "1h"}, "PUT")

                def received(status):
                    for path in notifications.iterdir():
                        try:
                            data = json.loads(path.read_text())
                            if data.get("status") == status and any(
                                alert.get("labels", {}).get("alertname") == "T004 API panic"
                                for alert in data.get("alerts", [])):
                                return True
                        except ValueError:
                            pass
                    return False

                panic = fixture(project, "api", json.dumps({"event": "http_panic", "level": "error"}))
                wait(lambda: received("firing"), "Grafana规则真实触发并投递本地Webhook")
                run(["docker", "rm", "-f", panic])
                fixtures.remove(panic)
                wait(lambda: received("resolved"), "故障消失后发送恢复通知")
            print("PASS 固定数据源与预置看板；project=" + project, flush=True)
        finally:
            for name in fixtures:
                subprocess.run(["docker", "rm", "-f", name], capture_output=True, timeout=30)
            subprocess.run(compose + ["down"], env=env, check=True, timeout=120)
            # 保留本次专用卷供排障；不删除业务卷，不输出临时凭据。


if __name__ == "__main__":
    main()
