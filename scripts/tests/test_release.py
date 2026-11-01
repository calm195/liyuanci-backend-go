"""生产配置跨进程一致性与只读诊断边界。"""
import importlib.util
import json
from pathlib import Path
import sys
import tempfile
import unittest
from types import SimpleNamespace

SCRIPTS = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(SCRIPTS))
spec = importlib.util.spec_from_file_location("release", SCRIPTS / "release.py")
release = importlib.util.module_from_spec(spec)
spec.loader.exec_module(release)


class ReleaseTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.environment = {"LIYUANCI_BUSINESS_IMAGE": "registry.invalid/business@sha256:" + "a" * 64}
        self.runtime = {key: "synthetic-" + key for key in release.SHARED}
        self.runtime["Enabled"] = True
        for suffix in release.PROCESSES.values():
            directory = Path(self.temp.name) / suffix
            directory.mkdir()
            (directory / "runtime.json").write_text(json.dumps({"Runtime": self.runtime}))
            self.environment["LIYUANCI_BUSINESS_" + suffix + "_DIR"] = str(directory)

    def test_key_drift_fails_without_disclosing_values(self):
        directory = Path(self.environment["LIYUANCI_BUSINESS_CARD_DIR"])
        runtime = dict(self.runtime, PrivacyKey="secret-not-for-output")
        (directory / "runtime.json").write_text(json.dumps({"Runtime": runtime}))
        with self.assertRaises(release.OperationError) as raised:
            release.inventory(self.environment)
        self.assertIn("PrivacyKey", str(raised.exception))
        self.assertNotIn("secret-not-for-output", str(raised.exception))

    def test_no_mutable_image_or_shared_credentials_directory(self):
        nested = Path(self.environment["LIYUANCI_BUSINESS_API_DIR"]) / "card"
        nested.mkdir()
        for field, value in [("LIYUANCI_BUSINESS_IMAGE", "business:latest"),
                             ("LIYUANCI_BUSINESS_CARD_DIR", self.environment["LIYUANCI_BUSINESS_API_DIR"]),
                             ("LIYUANCI_BUSINESS_CARD_DIR", str(nested))]:
            with self.subTest(field=field), self.assertRaises(release.OperationError):
                release.inventory(dict(self.environment, **{field: value}))

    def test_diagnostics_never_initialize_or_connect(self):
        image, directories = release.inventory(self.environment)
        calls = []
        def execute(command, **kwargs):
            calls.append(command)
            return SimpleNamespace(returncode=0)
        release.validate_containers(image, directories, execute)
        self.assertEqual(len(calls), 7)
        for command in calls:
            self.assertIn("--check-config".lstrip("-"), [part.lstrip("-") for part in command])
            self.assertEqual(command[command.index("--network") + 1], "none")
            self.assertIn("--read-only", command)
            self.assertIn("--pull=never", command)
            self.assertNotIn("-wordlists", command)
            self.assertNotIn("-login", command)

    def test_process_failure_is_not_success(self):
        image, directories = release.inventory(self.environment)
        with self.assertRaises(release.OperationError):
            release.validate_containers(image, directories, lambda *a, **k: SimpleNamespace(returncode=1))


if __name__ == "__main__":
    unittest.main()
