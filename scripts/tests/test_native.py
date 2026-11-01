"""原生入口验证覆盖拒绝覆盖、制品漂移、错误服务归属和只读默认行为。"""
import hashlib
import json
import os
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import native


class NativeTest(unittest.TestCase):
    def test_world_readable_or_linked_private_configuration_is_rejected(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            root.chmod(0o750)
            file = root / "runtime.json"
            file.write_text("{}")
            file.chmod(0o640)
            native.check_private_directory(root, os.getgid(), os.getuid())
            file.chmod(0o644)
            with self.assertRaises(native.OperationError):
                native.check_private_directory(root, os.getgid(), os.getuid())
            file.chmod(0o640)
            (root / "link").symlink_to(file)
            with self.assertRaises(native.OperationError):
                native.check_private_directory(root, os.getgid(), os.getuid())

    def test_templates_are_private_scoped_and_never_overwritten(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory) / "private"
            native.scaffold(root)
            self.assertEqual((root / "api/runtime.json").stat().st_mode & 0o777, 0o600)
            for kind in native.PROCESSES:
                config = json.loads((root / kind / "runtime.json").read_text())
                self.assertEqual(config["Runtime"]["TaskTransport"], "postgres")
                self.assertFalse(config["Runtime"]["DatabasePolling"])
                self.assertFalse(config["Runtime"]["ReviewMQFile"])
                self.assertFalse(config["Runtime"]["UnlockMQFile"])
                self.assertFalse(config["CardMQFile"])
                self.assertFalse(config["ContentMQFile"])
                self.assertEqual(config["Runtime"]["ReceiptKey"], "")
                if kind not in ("api", "bootstrap"):
                    self.assertNotIn("AdminDSN", config)
                    self.assertNotIn("WeChatSecretFile", config)
                self.assertEqual(config["Runtime"]["DatabaseMaxOpen"], 2)
            before = (root / "api/runtime.json").read_bytes()
            with self.assertRaises(native.OperationError):
                native.scaffold(root)
            self.assertEqual(before, (root / "api/runtime.json").read_bytes())

    def test_mutating_lifecycle_is_plan_only_by_default(self):
        with patch.object(native, "run", side_effect=AssertionError("不应执行systemctl")):
            for action in ("start", "stop", "restart"):
                plan = json.loads(native.lifecycle(action, "business", False))
                self.assertEqual(plan["command"], ["systemctl", action, "liyuanci-business.target"])

    def test_same_named_unrelated_unit_must_not_be_stopped(self):
        with patch.object(native, "run", return_value="/usr/lib/systemd/system/liyuanci-api.service\n"):
            with self.assertRaises(native.OperationError):
                native.lifecycle("stop", "business", True)

    def test_manifest_tamper_and_extra_files_fail(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "bin").mkdir()
            files = {}
            for binary in ("server", "business-worker", "business-init", "migrate"):
                name = "bin/" + binary
                (root / name).write_bytes(b"synthetic")
                files[name] = hashlib.sha256(b"synthetic").hexdigest()
            (root / "manifest.json").write_text(json.dumps({"files": files}))
            native.verify_bundle(root)
            (root / "bin/server").write_bytes(b"changed")
            with self.assertRaises(native.OperationError):
                native.verify_bundle(root)
            (root / "bin/server").write_bytes(b"synthetic")
            (root / "unexpected").write_text("drift")
            with self.assertRaises(native.OperationError):
                native.verify_bundle(root)

    def test_child_failure_never_discloses_output(self):
        from types import SimpleNamespace
        with patch.object(native.subprocess, "run", return_value=SimpleNamespace(returncode=1, stdout="secret", stderr="secret")):
            with self.assertRaises(native.OperationError) as raised:
                native.run(["systemctl", "start", "liyuanci.target"])
            self.assertNotIn("secret", str(raised.exception))


if __name__ == "__main__":
    unittest.main()
