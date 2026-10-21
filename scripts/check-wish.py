#!/usr/bin/env python3
"""T014/REQ-003、004、005、018：隔离PostgreSQL草稿持久化检查，密钥为合成适配器。"""
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
    name = "liyuanci-wish-check-" + uuid.uuid4().hex[:12]
    image = re.search(r"image:\s*(postgres:[^\s]+)", (root / "compose.integration.yaml").read_text()).group(1)
    if "@sha256:" not in image:
        raise RuntimeError("检查必须使用锁定镜像")
    password = secrets.token_hex(24)
    env = dict(os.environ, POSTGRES_PASSWORD=password)
    started = False
    print("心愿隔离检查项目：" + name, flush=True)
    try:
        subprocess.run(["docker", "run", "-d", "--name", name,
                        "--label", "com.docker.compose.project=" + name,
                        "--tmpfs", "/var/lib/postgresql", "-p", "127.0.0.1::5432",
                        "-e", "POSTGRES_PASSWORD", "-e", "POSTGRES_DB=wish", image],
                       env=env, check=True, stdout=subprocess.DEVNULL)
        started = True
        for _ in range(60):
            if subprocess.run(["docker", "exec", name, "pg_isready", "-U", "postgres", "-d", "wish"],
                              stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL).returncode == 0:
                break
            time.sleep(1)
        else:
            raise RuntimeError("隔离数据库未就绪")
        state = json.loads(subprocess.check_output(["docker", "inspect", name]))[0]
        port = state["NetworkSettings"]["Ports"]["5432/tcp"][0]["HostPort"]
        env["APP_WISH_TEST_DSN"] = "host=127.0.0.1 port=" + port + " user=postgres password=" + password + " dbname=wish sslmode=disable"
        subprocess.run(["go", "test", "-race", "-cover", "-count=1", "-timeout=180s", "-v", "./internal/wish"],
                       cwd=root, env=env, check=True)
        subprocess.run(["docker", "exec", name, "createdb", "-U", "postgres", "wish_http"],
                       check=True, stdout=subprocess.DEVNULL)
        env["APP_WISH_HTTP_TEST_DSN"] = env["APP_WISH_TEST_DSN"].replace("dbname=wish", "dbname=wish_http")
        subprocess.run(["go", "test", "-race", "-cover", "-count=1", "-timeout=180s", "-run", "TestPostgresWishHTTP", "-v", "./internal/httpapi"],
                       cwd=root, env=env, check=True)
    finally:
        # 只清理本次创建的临时容器，不操作其他项目或删除数据卷。
        if started:
            subprocess.run(["docker", "stop", name], check=True, stdout=subprocess.DEVNULL)
            subprocess.run(["docker", "rm", name], check=True, stdout=subprocess.DEVNULL)
    print("心愿隔离检查完成；实际密钥供应商、跨端和生产未验证", flush=True)


if __name__ == "__main__":
    main()
