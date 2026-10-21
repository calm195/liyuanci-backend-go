#!/usr/bin/env python3
"""T007/REQ-020：仅在随机独立 Compose 项目中验证真实 MQ 适配，不复用业务资源。"""
import os
import pathlib
import secrets
import subprocess
import tempfile
import uuid


def main():
    root = pathlib.Path(__file__).resolve().parents[1]
    project = "liyuanci-mq-check-" + uuid.uuid4().hex[:12]
    env = dict(os.environ, COMPOSE_PROJECT_NAME=project,
               LIYUANCI_TEST_DB_PASSWORD=secrets.token_hex(24))
    compose = ["docker", "compose", "-f", str(root / "compose.integration.yaml")]

    def run(args, **kwargs):
        return subprocess.run(args, cwd=root, env=env, check=True, **kwargs)

    print("真实 MQ 隔离项目：" + project, flush=True)
    with tempfile.TemporaryDirectory(prefix="liyuanci-mq-") as directory:
        binary = pathlib.Path(directory) / "mq.test"
        build_env = dict(env, GOOS="linux", CGO_ENABLED="0")
        # 原生 Docker 架构由服务器报告，支持 macOS ARM 与 Linux AMD runner。
        architecture = subprocess.check_output(["docker", "version", "--format", "{{.Server.Arch}}"], env=env, text=True).strip()
        build_env["GOARCH"] = architecture
        subprocess.run(["go", "test", "-c", "-o", str(binary), "./internal/transportmq"], cwd=root, env=build_env, check=True)
        try:
            run(compose + ["up", "-d", "--wait", "--wait-timeout", "180", "namesrv", "broker"])
            run(compose + ["exec", "-T", "broker", "sh", "mqadmin", "updateTopic", "-n", "namesrv:9876", "-c", "DefaultCluster", "-t", "durable_test"])
            # 二进制在本次项目内部网络运行，无宿主端口、真实身份或领域数据。
            run(["docker", "run", "--rm", "--label", "com.docker.compose.project=" + project,
                 "--network", project + "_foundation", "--read-only", "--cap-drop", "ALL",
                 "-e", "APP_MQ_TEST_NAMESERVER=namesrv:9876", "-v", str(binary) + ":/mq.test:ro",
                 "--entrypoint", "/mq.test",
                 "alpine:3.22@sha256:5291449c3df73caf6ed85e649dec1b9e818b39a5d8c871e97afc13e9cd5e8fa8",
                 "-test.run", "^TestBrokerRoundTrip$", "-test.v", "-test.timeout", "120s"])
        finally:
            # 永远保留卷；只停止本次随机项目，不使用 force/prune。
            run(compose + ["down"])
    print("真实 MQ 适配检查完成；不代表领域消费或整体验收通过", flush=True)


if __name__ == "__main__":
    main()
