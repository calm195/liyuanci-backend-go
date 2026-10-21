#!/usr/bin/env python3
"""T008/REQ-018、019：随机隔离真实PostgreSQL/Redis验证后台身份，不部署业务。"""
import json
import os
import pathlib
import re
import secrets
import subprocess
import time
import uuid


def main():
    root = pathlib.Path(__file__).resolve().parents[1]
    project = "liyuanci-admin-check-" + uuid.uuid4().hex[:12]
    compose = (root / "compose.integration.yaml").read_text()
    postgres = re.search(r"image:\s*(postgres:[^\s]+)", compose).group(1)
    redis = re.search(r"image:\s*(redis:[^\s]+)", compose).group(1)
    if not all("@sha256:" in image for image in (postgres, redis)):
        raise RuntimeError("检查必须使用锁定镜像")
    password = secrets.token_hex(24)
    env = dict(os.environ, POSTGRES_PASSWORD=password)
    started = []
    network_created = False
    print("后台身份隔离项目：" + project, flush=True)

    def run(args, **kwargs):
        return subprocess.run(args, env=env, check=True, **kwargs)

    try:
        run(["docker", "network", "create", "--label", "com.docker.compose.project=" + project, project], stdout=subprocess.DEVNULL)
        network_created = True
        for suffix, image, port in (("pg", postgres, "5432"), ("redis", redis, "6379")):
            name = project + "-" + suffix
            arguments = ["docker", "run", "-d", "--name", name, "--label", "com.docker.compose.project=" + project,
                         "--network", project, "-p", "127.0.0.1::" + port]
            if suffix == "pg":
                arguments += ["-e", "POSTGRES_PASSWORD", "-e", "POSTGRES_DB=adminauth"]
            run(arguments + [image], stdout=subprocess.DEVNULL)
            started.append(name)
        for _ in range(60):
            pg = subprocess.run(["docker", "exec", project + "-pg", "pg_isready", "-U", "postgres", "-d", "adminauth"], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            cache = subprocess.run(["docker", "exec", project + "-redis", "redis-cli", "ping"], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            if pg.returncode == 0 and cache.returncode == 0:
                break
            time.sleep(1)
        else:
            raise RuntimeError("隔离资源未就绪")
        def port(name, service):
            state = json.loads(subprocess.check_output(["docker", "inspect", name]))[0]
            return state["NetworkSettings"]["Ports"][service + "/tcp"][0]["HostPort"]
        env["APP_ADMINAUTH_TEST_DSN"] = "host=127.0.0.1 port=" + port(project + "-pg", "5432") + " user=postgres password=" + password + " dbname=adminauth sslmode=disable"
        env["APP_ADMINAUTH_TEST_REDIS"] = "127.0.0.1:" + port(project + "-redis", "6379")
        run(["go", "test", "-race", "-cover", "-count=1", "-timeout=180s", "-v", "./internal/adminauth"], cwd=root)
    finally:
        # 仅停止/移除本次专用容器，保留卷，不使用force或prune。
        for name in reversed(started):
            run(["docker", "stop", name], stdout=subprocess.DEVNULL)
            run(["docker", "rm", name], stdout=subprocess.DEVNULL)
        if network_created:
            run(["docker", "network", "rm", project], stdout=subprocess.DEVNULL)
    print("后台身份真实检查完成；微信、年级字典、生产TTL及整体验收不在范围", flush=True)


if __name__ == "__main__":
    main()
