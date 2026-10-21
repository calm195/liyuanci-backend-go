"""T004/REQ-018、020：启停边界、资源归属、显式迁移和凭据保留回归。"""
import contextlib
import io
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

import services


class Docker:
    def __init__(self):
        self.calls = []
        self.volumes = []
        self.rows = {"liyuanci-dev": [["api-id", "api"], ["worker-id", "worker"], ["pg-id", "postgres"],
                     ["mq-id", "broker"], ["ns-id", "namesrv"]],
                     "liyuanci-observability-dev": [["alloy-id", "alloy"], ["loki-id", "loki"], ["grafana-id", "grafana"]]}
        self.stopped = set()
        self.removed = set()

    def __call__(self, command, **kwargs):
        self.calls.append((command, kwargs))
        output = ""
        if command[1:3] == ["volume", "ls"]:
            output = "\n".join(self.volumes)
        elif command[1] == "ps":
            project = command[command.index("--filter") + 1].rsplit("=", 1)[1]
            output = "\n".join(" ".join(row) for row in self.rows.get(project, []) if
                               row[0] not in self.removed and ("-a" in command or row[0] not in self.stopped))
        elif command[1] == "stop":
            self.stopped.update(command[4:])
        elif command[1] == "rm":
            assert set(command[2:]) <= self.stopped
            self.removed.update(command[2:])
        return subprocess.CompletedProcess(command, 0, output, "")


class ServicesTest(unittest.TestCase):
    def setUp(self):
        self.output = io.StringIO()
        self.capture = contextlib.redirect_stdout(self.output)
        self.capture.__enter__()
        self.addCleanup(self.capture.__exit__, None, None, None)
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        self.root = Path(directory.name).resolve()
        # T004/REQ-020：隔离宿主默认凭据文件，防止本机开发配置覆盖测试输入。
        home = patch.object(services.Path, "home", return_value=self.root)
        home.start()
        self.addCleanup(home.stop)

    def service(self, command, env=None, docker=None):
        return services.Services(services.parser().parse_args(command), env or {}, docker or Docker())

    def credentials(self):
        env = {}
        for name in ("LIYUANCI_TEST_DB_PASSWORD_FILE", "LIYUANCI_PGADMIN_PASSWORD_FILE",
                     "LIYUANCI_DASHBOARD_PROPERTIES_FILE", "LIYUANCI_GRAFANA_PASSWORD_FILE"):
            path = self.root / name
            path.write_text("synthetic-private-value")
            env[name] = str(path)
        return env

    def test_explicit_bootstrap_and_preflight_order(self):
        default = self.service(["start"]).start_plan()
        self.assertFalse(any("migrate" in command or "probe" in command for command, _ in default))
        plan = self.service(["start", "--bootstrap-probe", "--probe"]).start_plan()
        stages = [stage for _, stage in plan]
        self.assertLess(stages.index("监控配置校验"), stages.index("启动中间件"))
        self.assertLess(stages.index("启动中间件"), stages.index("显式迁移探针表"))
        self.assertLess(stages.index("显式创建探针主题"), stages.index("启动API与worker"))
        self.assertLess(stages.index("启动API与worker"), stages.index("验证探针往返"))
        reused = self.service(["start", "--no-build"]).start_plan()
        self.assertTrue(any("--no-build" in command and "api" in command for command, _ in reused))

    def test_production_never_runs_business_or_probe(self):
        for command, _ in self.service(["start", "production"]).start_plan():
            self.assertFalse({"api", "worker", "migrate", "probe"} & set(command))
        docker = Docker()
        with self.assertRaises(services.OperationError):
            self.service(["start", "production", "--probe"], docker=docker).start()
        self.assertFalse(docker.calls)

    def test_production_preflight_with_external_files_only_starts_infrastructure(self):
        env = self.credentials()
        for name in ("LIYUANCI_POSTGRES_PASSWORD_FILE", "LIYUANCI_REDIS_ACL_FILE",
                     "LIYUANCI_MQ_ACL_FILE", "LIYUANCI_GRAFANA_ENCRYPTION_KEY_FILE"):
            path = self.root / name
            path.write_text("synthetic-external-content")
            env[name] = str(path)
        env.update(LIYUANCI_PGADMIN_EMAIL="operator@example.com", LIYUANCI_LOG_HOST="isolated",
                   LIYUANCI_GRAFANA_ROOT_URL="http://127.0.0.1:13000/", LIYUANCI_LOG_RETENTION="336h")
        docker = Docker()
        self.assertEqual(self.service(["start", "production"], env, docker).start(), "started")
        for command, _ in docker.calls:
            self.assertFalse({"api", "worker", "migrate", "probe"} & set(command))
        self.assertFalse(any("run" in command for command, _ in docker.calls))

    def test_preflight_missing_monitor_secrets_has_no_side_effect(self):
        env = self.credentials()
        env["LIYUANCI_GRAFANA_PASSWORD_FILE"] = str(self.root / "missing")
        docker = Docker()
        with self.assertRaises(services.OperationError):
            self.service(["start"], env, docker).start()
        self.assertFalse(docker.calls)

    def test_password_file_overrides_stale_export(self):
        env = self.credentials()
        env["LIYUANCI_TEST_DB_PASSWORD"] = "stale-shell-password"
        docker = Docker()
        self.service(["start"], env, docker).start()
        self.assertTrue(docker.calls)
        self.assertTrue(all(kwargs["env"]["LIYUANCI_TEST_DB_PASSWORD"] == "synthetic-private-value"
                            for _, kwargs in docker.calls))

    def test_stop_without_credentials_preserves_shutdown_logs_and_volumes(self):
        docker = Docker()
        service = self.service(["stop", "--remove"], docker=docker)
        self.assertEqual(service.stop(), "stopped")
        commands = [command for command, _ in docker.calls]
        stops = [command[4:] for command in commands if command[1] == "stop"]
        self.assertLess(next(i for i, ids in enumerate(stops) if "api-id" in ids),
                        next(i for i, ids in enumerate(stops) if "alloy-id" in ids))
        self.assertLess(next(i for i, ids in enumerate(stops) if "alloy-id" in ids),
                        next(i for i, ids in enumerate(stops) if "loki-id" in ids))
        self.assertLess(next(i for i, ids in enumerate(stops) if "loki-id" in ids),
                        next(i for i, ids in enumerate(stops) if "pg-id" in ids))
        self.assertFalse(any(command[1:3] == ["volume", "rm"] or "--force" in command for command in commands))
        self.assertEqual(service.stop(), "stopped")

    def test_existing_volume_missing_password_refuses_init(self):
        docker = Docker()
        docker.volumes = ["liyuanci-dev_pg-data"]
        directory = self.root / "new-config"
        with self.assertRaises(services.OperationError):
            self.service(["init-dev", "--config-dir", str(directory)], docker=docker).init_dev()
        self.assertFalse(directory.exists())

    def test_init_never_overwrites_existing_credentials(self):
        directory = self.root / "config"
        service = self.service(["init-dev", "--config-dir", str(directory)])
        service.init_dev()
        before = {str(path): path.read_bytes() for path in directory.rglob("*") if path.is_file()}
        service.init_dev()
        after = {str(path): path.read_bytes() for path in directory.rglob("*") if path.is_file()}
        self.assertEqual(before, after)
        self.assertEqual(directory.stat().st_mode & 0o777, 0o700)

    def test_env_is_literal_and_cannot_change_path(self):
        filename = self.root / "operations.env"
        marker = self.root / "executed"
        filename.write_text("LIYUANCI_LOG_HOST=$(touch " + str(marker) + ")\nLIYUANCI_GRAFANA_PASSWORD_FILE=local-file\n")
        env = services.load_env(filename, {})
        self.assertEqual(env["LIYUANCI_LOG_HOST"], "$(touch " + str(marker) + ")")
        self.assertEqual(env["LIYUANCI_GRAFANA_PASSWORD_FILE"], str(self.root / "local-file"))
        self.assertFalse(marker.exists())
        filename.write_text("PATH=/untrusted\n")
        with self.assertRaises(services.OperationError):
            services.load_env(filename, {})

    def test_invalid_encoding_does_not_leak_secret(self):
        filename = self.root / "broken.env"
        filename.write_bytes(b"LIYUANCI_LOG_HOST=private-secret-\xff")
        errors = io.StringIO()
        with contextlib.redirect_stderr(errors):
            code = services.main(["start", "--env-file", str(filename)])
        self.assertEqual(code, 1)
        self.assertNotIn("private-secret", errors.getvalue() + self.output.getvalue())
        self.assertIn('"result": "failed"', self.output.getvalue())

    def test_docker_failure_never_prints_secret_or_success(self):
        env = self.credentials()
        docker = lambda command, **_: subprocess.CompletedProcess(command, 1, "synthetic-private-value", "secret-dsn")
        with patch.dict(os.environ, env, clear=True), patch("services.subprocess.run", docker), contextlib.redirect_stderr(io.StringIO()):
            code = services.main(["start"])
        self.assertEqual(code, 1)
        self.assertNotIn("synthetic-private-value", self.output.getvalue())
        self.assertNotIn("secret-dsn", self.output.getvalue())
        self.assertNotIn('"result": "started"', self.output.getvalue())

    def test_cross_environment_project_and_same_project_rejected(self):
        for command in (["stop", "dev", "--project", "liyuanci-production"],
                        ["stop", "--project", "same", "--monitor-project", "same"]):
            with self.assertRaises(services.OperationError):
                self.service(command)

    def test_shell_entry_works_outside_repository_without_docker(self):
        filename = self.root / "empty.env"
        filename.write_text("")
        result = subprocess.run([str(services.ROOT / "scripts/start.sh"), "dev", "--env-file", str(filename), "--dry-run"],
                                cwd=self.root, text=True, capture_output=True, check=True)
        records = [json.loads(line) for line in result.stdout.splitlines()]
        self.assertEqual(records[-1]["result"], "planned")
        self.assertTrue(all(str(services.ROOT) in " ".join(row["argv"]) for row in records[:-1]))


if __name__ == "__main__":
    unittest.main()
