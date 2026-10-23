#!/usr/bin/env python3
"""Docker环境生命周期入口，不执行生产部署或隐式迁移。"""
import argparse
import json
import os
from pathlib import Path
import re
import secrets
import subprocess
import sys
import time

from configuration import ROOT, directory, import_legacy, private_path
MANAGEMENT = ["pgadmin", "redisinsight", "rocketmq-dashboard"]
MIDDLEWARE = ["postgres", "redis", "namesrv", "broker"]


def load_env(path, environment):
    """配置只接受字面KEY=value，不执行shell、变量替换或命令替换。"""
    result = environment.copy()
    seen = set()
    for number, line in enumerate(Path(path).read_text().splitlines(), 1):
        line = line.strip()
        if not line or line.startswith("#"):
            continue
        key, separator, value = line.partition("=")
        key, value = key.strip(), value.strip()
        if not separator or not re.fullmatch(r"(?:LIYUANCI|APP)_[A-Z0-9_]+", key) or key in seen:
            raise OperationError("环境文件第%d行格式、变量名或重复定义有误" % number)
        if value.startswith(("'", '"')):
            if len(value) < 2 or value[-1] != value[0]:
                raise OperationError("环境文件第%d行引号不匹配" % number)
            value = value[1:-1]
        if "\x00" in value:
            raise OperationError("环境文件包含无效字符")
        if key.endswith("_FILE") and value:
            filename = Path(value).expanduser()
            result[key] = str((Path(path).resolve().parent / filename).resolve())
        else:
            result[key] = value
        seen.add(key)
    return result


class OperationError(Exception):
    """只承载可以安全显示的操作错误，不包含子进程或文件正文。"""


class Services:
    """将Docker命令与凭据环境分开，便于验证失败边界和资源归属。"""

    def __init__(self, args, environment=None, execute=None):
        self.args = args
        self.env = dict(os.environ if environment is None else environment)
        self.execute = execute or subprocess.run
        mode = getattr(args, "environment", "dev")
        self.mode = mode
        self.project = args.project or ("liyuanci-dev" if mode == "dev" else "liyuanci-production")
        self.monitor = args.monitor_project or (
            self.project + "-observability" if args.project else "liyuanci-observability-" + mode)
        for name in (self.project, self.monitor):
            if not re.fullmatch(r"[a-z0-9][a-z0-9_-]*", name):
                raise OperationError("项目名必须为小写字母、数字、下划线或连字符")
        if self.project == self.monitor:
            raise OperationError("中间件与监控必须使用不同项目名")
        reserved = ("liyuanci-production", "liyuanci-observability-production") if mode == "dev" else (
            "liyuanci-dev", "liyuanci-observability-dev")
        if self.project in reserved or self.monitor in reserved:
            raise OperationError("不能使用另一环境的默认项目名")

    def call(self, command, stage, missing_ok=False):
        # 不打印环境、展开配置或子进程错误正文，避免Docker错误携带凭据。
        started = time.monotonic()
        succeeded = False
        try:
            result = self.execute(command, cwd=ROOT, env=self.env, text=True,
                                  capture_output=True, timeout=900)
            succeeded = result.returncode == 0
            if not succeeded and not missing_ok:
                raise OperationError(stage + "失败（Docker退出码%d）；资源保留，可按README查看对应项目日志" % result.returncode)
            return result
        finally:
            print(json.dumps({"msg": "运行阶段执行结束", "stage": stage,
                              "result": "completed" if succeeded else "failed",
                              "duration": round(time.monotonic() - started, 3)}, ensure_ascii=False))

    def docker_ready(self):
        self.call(["docker", "info", "--format", "{{.ServerVersion}}"], "Docker引擎检查")

    def defaults(self):
        base = directory("dev")
        defaults = {
            "LIYUANCI_TEST_DB_PASSWORD_FILE": str(base / "postgres-password"),
            "LIYUANCI_PGADMIN_PASSWORD_FILE": str(base / "pgadmin-password"),
            "LIYUANCI_DASHBOARD_PROPERTIES_FILE": str(base / "dashboard.properties"),
            "LIYUANCI_GRAFANA_PASSWORD_FILE": str(base / "observability/grafana-password"),
            "LIYUANCI_PGADMIN_EMAIL": "developer@example.com",
        }
        if self.mode == "dev":
            for key, value in defaults.items():
                self.env.setdefault(key, value)
        self.env.setdefault("LIYUANCI_LOG_PROJECT", self.project)
        self.env.setdefault("LIYUANCI_LOG_ENVIRONMENT", "development" if self.mode == "dev" else "production")
        self.env.setdefault("LIYUANCI_MONITORED_NETWORK", self.project + "_foundation")
        if self.mode == "dev":
            self.env.setdefault("LIYUANCI_LOG_HOST", "local-dev")
            self.env.setdefault("LIYUANCI_LOG_RETENTION", "72h")

    def required(self, key):
        value = self.env.get(key, "")
        if not value:
            raise OperationError("缺少配置：" + key)
        return value

    def secret_file(self, key, empty=False):
        path = private_path(self.required(key))
        if not path.is_file() or (not empty and path.stat().st_size == 0):
            raise OperationError(key + "对应文件不存在或为空")
        self.env[key] = str(path)
        return path

    def compose(self, monitoring=False):
        files = ["deploy/compose/observability.yaml"] if monitoring else [
            "deploy/compose/integration.yaml" if self.mode == "dev" else "deploy/compose/production.yaml"]
        if not monitoring and self.mode == "dev" and self.env.get("LIYUANCI_DEV_BUSINESS_FILE"):
            files.append("deploy/compose/business-dev.yaml")
        if monitoring:
            if self.mode == "production":
                files.append("deploy/compose/observability.production.yaml")
            files.extend(["deploy/compose/alerting.yaml", "deploy/compose/metrics.yaml"])
            if self.args.middleware_metrics:
                files.append("deploy/compose/metrics.middleware.yaml")
            if self.args.linux_host:
                files.append("deploy/compose/metrics.linux.yaml")
            if self.args.notifications:
                files.append("deploy/compose/notifications.yaml")
        elif not self.args.without_management:
            files.append("deploy/compose/management.yaml")
        command = ["docker", "compose", "-p", self.monitor if monitoring else self.project]
        for name in files:
            command += ["-f", str(ROOT / name)]
        if not monitoring and not self.args.without_management:
            command += ["--profile", "management"]
        return command

    def start_plan(self):
        primary = self.compose()
        plan = [(primary + ["config", "--quiet"], "主项目配置校验")]
        if not self.args.without_monitoring:
            plan.append((self.compose(True) + ["config", "--quiet"], "监控配置校验"))
        plan.append((primary + ["up", "-d", "--wait", "--wait-timeout", "240"] + MIDDLEWARE, "启动中间件"))
        if self.mode == "dev":
            if self.args.bootstrap_probe:
                plan += [(primary + ["run", "--rm", "migrate"], "显式迁移探针表"),
                         (primary + ["exec", "-T", "broker", "sh", "mqadmin", "updateTopic", "-n", "namesrv:9876",
                                     "-c", "DefaultCluster", "-t", "liyuanci_foundation_probe_v1"], "显式创建探针主题")]
            plan.append((primary + ["up", "-d", "--wait", "--wait-timeout", "120"] +
                         (["--no-build"] if self.args.no_build else ["--build"]) + ["api", "worker"] + (["card-worker"] if self.env.get("LIYUANCI_DEV_BUSINESS_FILE") else []), "启动API与worker"))
            if self.args.probe:
                plan.append((primary + ["run", "--rm", "probe"], "验证探针往返"))
        if not self.args.without_management:
            plan.append((primary + ["up", "-d", "--wait", "--wait-timeout", "240"] + MANAGEMENT, "启动管理工具"))
        if not self.args.without_monitoring:
            plan.append((self.compose(True) + ["up", "-d", "--wait", "--wait-timeout", "120"], "启动监控"))
        return plan

    def start(self):
        if self.mode == "production" and (self.args.bootstrap_probe or self.args.probe or self.args.no_build):
            raise OperationError("生产骨架不包含API/worker、迁移或探针参数")
        if self.args.without_monitoring and (self.args.middleware_metrics or self.args.linux_host or self.args.notifications):
            raise OperationError("监控扩展参数不能与--without-monitoring组合")
        if self.args.linux_host and sys.platform != "linux":
            raise OperationError("--linux-host仅用于Linux宿主，不能用Docker VM冒充macOS宿主")
        path = self.args.env_file
        if not path and self.mode == "dev":
            if not self.args.dry_run:
                import_legacy("dev")
            default = directory("dev") / "operations.env"
            path = str(default) if default.is_file() else None
        if path:
            self.env = load_env(Path(path).expanduser(), self.env)
        self.defaults()
        plan = self.start_plan()
        if self.args.dry_run:
            for command, stage in plan:
                print(json.dumps({"stage": stage, "argv": command}, ensure_ascii=False))
            return "planned"
        if self.mode == "dev":
            # 文件是入口的权威配置，避免旧终端export覆盖新文件并初始化出不可复现的卷。
            password = self.secret_file("LIYUANCI_TEST_DB_PASSWORD_FILE").read_text().strip()
            if not password or any(char in password for char in "\r\n\x00"):
                raise OperationError("开发数据库口令文件必须为单行非空值")
            self.env["LIYUANCI_TEST_DB_PASSWORD"] = password
            if self.env.get("LIYUANCI_DEV_BUSINESS_FILE"):
                self.secret_file("LIYUANCI_DEV_BUSINESS_FILE")
            else:
                print("开发业务尚未初始化；当前仅健康入口。请显式运行 python3 scripts/init-business-dev.py --admin-login 自定登录名")
        else:
            for key in ("LIYUANCI_POSTGRES_PASSWORD_FILE", "LIYUANCI_REDIS_ACL_FILE", "LIYUANCI_MQ_ACL_FILE"):
                self.secret_file(key)
        if not self.args.without_management:
            self.required("LIYUANCI_PGADMIN_EMAIL")
            self.secret_file("LIYUANCI_PGADMIN_PASSWORD_FILE")
            self.secret_file("LIYUANCI_DASHBOARD_PROPERTIES_FILE", empty=self.mode == "dev")
        if not self.args.without_monitoring:
            for key in ("LIYUANCI_LOG_PROJECT", "LIYUANCI_LOG_ENVIRONMENT", "LIYUANCI_LOG_HOST"):
                self.required(key)
            if self.env["LIYUANCI_LOG_PROJECT"] != self.project:
                raise OperationError("日志目标必须与所选主项目一致")
            self.secret_file("LIYUANCI_GRAFANA_PASSWORD_FILE")
            if self.mode == "production":
                self.secret_file("LIYUANCI_GRAFANA_ENCRYPTION_KEY_FILE")
                self.required("LIYUANCI_GRAFANA_ROOT_URL")
                self.required("LIYUANCI_LOG_RETENTION")
            if self.args.middleware_metrics:
                if self.env["LIYUANCI_MONITORED_NETWORK"] != self.project + "_foundation":
                    raise OperationError("监控网络必须属于所选主项目")
                for key in ("LIYUANCI_PG_MONITOR_PASSWORD_FILE", "LIYUANCI_REDIS_MONITOR_PASSWORDS_FILE"):
                    self.secret_file(key)
            if self.args.notifications:
                self.secret_file("LIYUANCI_ALERT_NOTIFICATIONS_FILE")
        self.docker_ready()
        for command, stage in plan:
            self.call(command, stage)
        # Compose wait对无healthcheck服务仅验证运行状态，不将其描述为业务/告警联调成功。
        return "started"

    def containers(self, project, all_containers=False):
        command = ["docker", "ps"] + (["-a"] if all_containers else []) + [
            "--filter", "label=com.docker.compose.project=" + project,
            "--format", '{{.ID}} {{.Label "com.docker.compose.service"}}']
        return [line.split(maxsplit=1) for line in self.call(command, "读取项目容器").stdout.splitlines() if line.strip()]

    def stop(self):
        if self.args.dry_run:
            print(json.dumps({"projects": [self.project, self.monitor], "order": ["API/worker与管理工具", "监控", "中间件"],
                              "remove_containers_and_networks": self.args.remove, "remove_volumes": False}, ensure_ascii=False))
            return "planned"
        self.docker_ready()
        primary = self.containers(self.project)
        monitor = self.containers(self.monitor)

        def stop_rows(rows):
            if rows:
                self.call(["docker", "stop", "--time", "30"] + [row[0] for row in rows], "停止所选项目容器")

        # API/worker退出时采集链路仍在线；先停止exporter再关闭其依赖中间件。
        stop_rows([row for row in primary if len(row) < 2 or row[1] not in MIDDLEWARE + ["recovery-postgres"]])
        for group in ({"alloy", "postgres-exporter", "redis-exporter", "node-exporter"}, {"grafana", "prometheus"}, {"loki"}):
            stop_rows([row for row in monitor if len(row) > 1 and row[1] in group])
        stop_rows([row for row in monitor if len(row) < 2 or row[1] not in {
            "alloy", "postgres-exporter", "redis-exporter", "node-exporter", "grafana", "prometheus", "loki"}])
        for service in ("broker", "namesrv", "redis", "recovery-postgres", "postgres"):
            stop_rows([row for row in primary if len(row) > 1 and row[1] == service])
        if self.args.remove:
            for project in (self.monitor, self.project):
                rows = self.containers(project, True)
                if rows:
                    self.call(["docker", "rm"] + [row[0] for row in rows], "移除已停止容器")
            for project in (self.monitor, self.project):
                networks = self.call(["docker", "network", "ls", "--filter", "label=com.docker.compose.project=" + project,
                                      "--format", "{{.ID}}"], "读取项目网络").stdout.split()
                if networks:
                    self.call(["docker", "network", "rm"] + networks, "移除项目网络")
        return "stopped"

    def init_dev(self):
        base = private_path(self.args.config_dir) if self.args.config_dir else directory("dev")
        if not self.args.config_dir and not self.args.dry_run:
            import_legacy("dev")
        monitoring = base / "observability"
        paths = {"LIYUANCI_TEST_DB_PASSWORD_FILE": base / "postgres-password",
                 "LIYUANCI_PGADMIN_PASSWORD_FILE": base / "pgadmin-password",
                 "LIYUANCI_DASHBOARD_PROPERTIES_FILE": base / "dashboard.properties",
                 "LIYUANCI_GRAFANA_PASSWORD_FILE": monitoring / "grafana-password"}
        entry = base / "operations.env"
        if entry.is_file():
            previous = load_env(entry, {})
            paths.update({key: private_path(previous[key]) for key in paths if previous.get(key)})
        if any("\n" in str(path) or "\r" in str(path) for path in paths.values()):
            raise OperationError("凭据路径不能包含换行")
        for folder in (base, monitoring):
            private_path(folder)
        if self.args.dry_run:
            print(json.dumps({"create_missing_files": [str(path) for path in paths.values()], "overwrite": False}))
            return "planned"
        self.docker_ready()
        volumes = set(self.call(["docker", "volume", "ls", "--format", "{{.Name}}"], "检查既有数据卷").stdout.split())
        for key, volume in (("LIYUANCI_TEST_DB_PASSWORD_FILE", self.project + "_pg-data"),
                            ("LIYUANCI_PGADMIN_PASSWORD_FILE", self.project + "_pgadmin-data"),
                            ("LIYUANCI_GRAFANA_PASSWORD_FILE", self.monitor + "_grafana-data")):
            if (not paths[key].is_file() or paths[key].stat().st_size == 0) and volume in volumes:
                raise OperationError("已有数据卷但缺少原凭据文件，请恢复原文件，禁止生成新口令覆盖旧卷")
        for path in paths.values():
            if path.exists() and (not path.is_file() or (path.name != "dashboard.properties" and not path.stat().st_size)):
                raise OperationError("已有凭据路径无效，不覆盖已有文件")
        # 只创建缺失文件；拒绝放宽现有目录权限或重置已有账号。
        for folder in (base, monitoring):
            folder.mkdir(mode=0o700, parents=True, exist_ok=True)
            if folder.stat().st_mode & 0o077:
                raise OperationError("凭据目录权限过宽，请由操作者设置为0700")
        for path in paths.values():
            if not path.exists():
                with path.open("x") as output:
                    output.write("" if path.name == "dashboard.properties" else secrets.token_hex(24) + "\n")
                path.chmod(0o644)
        env_path = base / "operations.env"
        if not env_path.exists():
            with env_path.open("x") as output:
                for key, path in paths.items():
                    output.write(key + "=" + str(path) + "\n")
                output.write("LIYUANCI_PGADMIN_EMAIL=developer@example.com\n")
            env_path.chmod(0o600)
        print("开发配置入口：" + str(env_path))
        return "initialized"


def parser():
    command = argparse.ArgumentParser(description=__doc__)
    actions = command.add_subparsers(dest="action", required=True)
    for action in ("start", "stop", "init-dev"):
        child = actions.add_parser(action)
        if action != "init-dev":
            child.add_argument("environment", choices=["dev", "production"], nargs="?", default="dev")
        child.add_argument("--project")
        child.add_argument("--monitor-project")
        child.add_argument("--dry-run", action="store_true")
        if action == "start":
            child.add_argument("--env-file")
            for flag in ("without-management", "without-monitoring", "middleware-metrics", "linux-host",
                         "notifications", "bootstrap-probe", "probe", "no-build"):
                child.add_argument("--" + flag, action="store_true")
        elif action == "stop":
            child.add_argument("--remove", action="store_true")
        else:
            child.add_argument("--config-dir")
    return command


def main(argv=None):
    args = parser().parse_args(argv)
    started = time.monotonic()
    result = "failed"
    code = 1
    try:
        service = Services(args)
        result = getattr(service, args.action.replace("-", "_"))()
        code = 0
    except OperationError as error:
        print(str(error), file=sys.stderr)
    except (OSError, ValueError, RuntimeError, subprocess.SubprocessError):
        # 解码错误和Docker异常可能携带配置正文，只输出固定安全信息。
        print("读取配置或调用Docker失败，请检查文件、编码与Docker引擎；不会输出配置正文", file=sys.stderr)
    except KeyboardInterrupt:
        result, code = "interrupted", 130
        print("操作已中断；已启动的资源保留，可使用相同项目名执行stop", file=sys.stderr)
    finally:
        print(json.dumps({"msg": "运维操作执行结束", "operation": args.action,
                          "environment": getattr(args, "environment", "dev"), "result": result,
                          "duration": round(time.monotonic() - started, 3)}, ensure_ascii=False))
    return code


if __name__ == "__main__":
    raise SystemExit(main())
