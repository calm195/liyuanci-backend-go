#!/usr/bin/env python3
"""T005/REQ-020：随机专用 Docker PostgreSQL 迁移验证，不操作已有项目。"""
import argparse
import json
import os
import secrets
import subprocess
import time
import uuid


def check_container(name, password, task_root):
    """T005/REQ-018、020：运行正式镜像内的 CLI，不输出连接参数或驱动原文。"""
    tag = "liyuanci-migrate-check:" + uuid.uuid4().hex[:12]
    build = ["docker", "build", "-t", tag]
    if os.environ.get("LIYUANCI_BUILD_GOPROXY"):
        build += ["--build-arg", "GOPROXY=" + os.environ["LIYUANCI_BUILD_GOPROXY"]]
    subprocess.run(build + [str(task_root)], check=True)
    # 加密库尚未接入 API 二进制，额外使用锁定工具链执行库测试，不能只由 CLI 构建推断兼容。
    builder_tag = tag + "-builder"
    builder_build = ["docker", "build", "--target", "builder", "-t", builder_tag]
    if os.environ.get("LIYUANCI_BUILD_GOPROXY"):
        builder_build += ["--build-arg", "GOPROXY=" + os.environ["LIYUANCI_BUILD_GOPROXY"]]
    subprocess.run(builder_build + [str(task_root)], check=True)
    subprocess.run(["docker", "run", "--rm", "--network", "none", "--entrypoint", "go", builder_tag, "version"], check=True)
    subprocess.run(["docker", "run", "--rm", "--network", "none", "-e", "CGO_ENABLED=0", "--entrypoint", "go", builder_tag,
                    "test", "./internal/securedata", "./internal/identity", "./internal/command", "./internal/durable", "./internal/adminauth", "./internal/studentauth", "./internal/studentcard", "./internal/httpapi"], check=True)
    actual_user = subprocess.check_output(["docker", "run", "--rm", "--entrypoint", "id", tag, "-u"], text=True).strip()
    if actual_user != "10001":
        raise RuntimeError("镜像迁移工具未使用专用非 root 用户")
    for database in ("cli_business", "cli_recovery"):
        subprocess.run(["docker", "exec", name, "psql", "-U", "postgres", "-d", "postgres", "-v", "ON_ERROR_STOP=1", "-c", "CREATE DATABASE " + database], check=True, stdout=subprocess.DEVNULL)
    base_dsn = f"host=127.0.0.1 port=5432 user=postgres password={password} sslmode=disable dbname="
    cli_env = dict(os.environ, APP_DATABASE_DSN=base_dsn + "cli_business", APP_RECOVERY_DATABASE_DSN=base_dsn + "cli_recovery")

    def invoke(action, expected=0, supplied_env=None):
        # 仅共享本次 PostgreSQL 容器的网络；不继承宿主网络，不发布其他端口。
        command = ["docker", "run", "--rm", "--network", "container:" + name,
                   "-e", "APP_DATABASE_DSN", "-e", "APP_RECOVERY_DATABASE_DSN", "--entrypoint", "./migrate", tag, action]
        result = subprocess.run(command, env=supplied_env or cli_env, capture_output=True, text=True)
        output = result.stdout + result.stderr
        if password in output or base_dsn in output:
            raise RuntimeError("迁移命令泄漏数据库配置")
        records = []
        for line in output.splitlines():
            try:
                records.append(json.loads(line))
            except json.JSONDecodeError:
                pass
        wanted = "success" if expected == 0 else "failure"
        if result.returncode != expected or not any(r.get("msg") == "迁移命令完成" and r.get("result") == wanted and "duration" in r for r in records):
            raise RuntimeError("镜像迁移命令结果或完成日志不符：" + action)

    for action in ("up", "up", "permissions", "permissions", "down", "up", "permissions",
                   "journal-up", "journal-up", "journal-permissions", "journal-down", "journal-up", "journal-permissions"):
        invoke(action)
    mixed_env = dict(cli_env, APP_RECOVERY_DATABASE_DSN=cli_env["APP_DATABASE_DSN"])
    invoke("journal-up", expected=1, supplied_env=mixed_env)
    invoke("invalid-operation", expected=1)
    # 专用镜像标签保留，便于复核制品；不删除镜像、卷或其他项目资源。
    print("正式镜像 CLI 验证通过；用户10001、重复迁移/权限、回退、恢复库混建拒绝及日志脱敏；制品标签=" + tag)


def main():
    parser = argparse.ArgumentParser(description="专用 Docker 迁移/身份验证")
    parser.add_argument("--container", action="store_true", help="构建正式镜像并验证迁移 CLI")
    args = parser.parse_args()
    name = "liyuanci-migrations-" + uuid.uuid4().hex[:12]
    password = secrets.token_hex(24)
    # 固定镜像引用与既有集成环境一致，避免本工具自行选择版本。
    import pathlib
    import re
    compose = (pathlib.Path(__file__).resolve().parents[1] / "compose.integration.yaml").read_text()
    match = re.search(r"image:\s*(postgres:[^\s]+)", compose)
    if not match or "@sha256:" not in match.group(1):
        raise RuntimeError("缺少固定 PostgreSQL 镜像")
    image = match.group(1)
    env = dict(os.environ, POSTGRES_PASSWORD=password)
    started = False
    try:
        subprocess.run(["docker", "run", "-d", "--name", name, "--label", "com.docker.compose.project=" + name,
                        "-e", "POSTGRES_PASSWORD", "-e", "POSTGRES_DB=migrations", "-p", "127.0.0.1::5432", image],
                       env=env, check=True, stdout=subprocess.DEVNULL)
        started = True
        for _ in range(60):
            check = subprocess.run(["docker", "exec", name, "pg_isready", "-h", "127.0.0.1", "-U", "postgres", "-d", "migrations"],
                                   stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            if check.returncode == 0:
                break
            time.sleep(1)
        else:
            raise RuntimeError("隔离数据库启动超时")
        for database in ("business_schema", "recovery", "commands", "durable"):
            subprocess.run(["docker", "exec", name, "psql", "-U", "postgres", "-d", "postgres", "-v", "ON_ERROR_STOP=1", "-c", "CREATE DATABASE " + database], check=True, stdout=subprocess.DEVNULL)
        state = json.loads(subprocess.check_output(["docker", "inspect", name]))[0]
        port = state["NetworkSettings"]["Ports"]["5432/tcp"][0]["HostPort"]
        test_env = dict(os.environ, APP_MIGRATION_TEST_DSN=f"host=127.0.0.1 port={port} user=postgres password={password} dbname=migrations sslmode=disable")
        test_env["APP_SCHEMA_TEST_DSN"] = test_env["APP_MIGRATION_TEST_DSN"].replace("dbname=migrations", "dbname=business_schema")
        test_env["APP_RECOVERY_TEST_DSN"] = test_env["APP_MIGRATION_TEST_DSN"].replace("dbname=migrations", "dbname=recovery")
        test_env["APP_COMMAND_TEST_DSN"] = test_env["APP_MIGRATION_TEST_DSN"].replace("dbname=migrations", "dbname=commands")
        test_env["APP_DURABLE_TEST_DSN"] = test_env["APP_MIGRATION_TEST_DSN"].replace("dbname=migrations", "dbname=durable")
        # 迁移与身份按序运行；命令框架使用独立数据库，避免夹具互相干扰。
        for package in ("./internal/migration", "./internal/identity", "./internal/command", "./internal/durable"):
            subprocess.run(["go", "test", "-race", "-cover", "-count=1", "-v", package],
                           cwd=pathlib.Path(__file__).resolve().parents[1], env=test_env, check=True)
        if args.container:
            check_container(name, password, pathlib.Path(__file__).resolve().parents[1])
        print("隔离迁移验证通过；包含身份占用、双代轮换与合成权限组；包含业务表/证据域及独立逻辑恢复库；未验证生产身份、外部密钥或物理隔离恢复")
    finally:
        if started:
            # 仅移除本次随机容器，不删除数据卷，不使用 force/prune。
            subprocess.run(["docker", "stop", name], check=True, stdout=subprocess.DEVNULL)
            subprocess.run(["docker", "rm", name], check=True, stdout=subprocess.DEVNULL)


if __name__ == "__main__":
    main()
