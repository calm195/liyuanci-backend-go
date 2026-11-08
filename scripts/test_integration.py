"""验证原生集成不会把零用例、跳过或不完整事件当作通过，不启动外部依赖。"""
from contextlib import redirect_stdout
import io
import json
import re
from pathlib import Path
import subprocess
import unittest
from unittest.mock import patch

import integration


PACKAGE = 'github.com/calm195/liyuanci-backend-go/internal/modules/resourceplaza'


def event(action, test='', **extra):
    return dict(Package=PACKAGE, Action=action, Test=test, **extra)


class IntegrationResultTests(unittest.TestCase):
    def run_result(self, events, code=0, raw='', suite='resourceplaza'):
        output = ''.join(json.dumps(item) + '\n' for item in events) + raw
        result = subprocess.CompletedProcess(['go', 'test'], code, stdout=output)
        with patch.object(integration.subprocess, 'run', return_value=result), redirect_stdout(io.StringIO()) as stream:
            integration.run_suite(suite, Path('/unused/tests.yaml'), {})
        return stream.getvalue()

    def complete(self):
        return [event('run', 'TestPostgresHTTP'), event('pass', 'TestPostgresHTTP'), event('pass')]

    def test_moderation_integration_entries_are_registered(self):
        # 扫描实际集成入口，避免新增聊天/治理回归仅在普通单测中被跳过。
        folder = integration.ROOT / 'internal/modules/moderation'
        required = set()
        for source in folder.glob('*integration_test.go'):
            required.update(re.findall(
                r'^func (Test\w+)\(t \*testing.T\)', source.read_text(), re.MULTILINE
            ))
        registered = set(integration.SUITES['moderation'][1])
        self.assertTrue(required)
        self.assertFalse(required - registered, required - registered)

    def test_complete_run_preserves_test_output(self):
        events = self.complete()
        events.insert(1, event('output', 'TestPostgresHTTP', Output='测试说明\n'))
        output = self.run_result(events)
        self.assertIn('测试说明\n', output)
        self.assertIn('必需用例 1 项', output)

    def test_no_matching_tests_is_not_success(self):
        with self.assertRaisesRegex(ValueError, '缺少执行通过'):
            self.run_result([event('output', Output='testing: warning: no tests to run\n'), event('pass')])

    def test_skipped_test_or_subtest_is_not_success(self):
        for test in ('TestPostgresHTTP', 'TestPostgresHTTP/权限检查'):
            with self.subTest(test=test), self.assertRaisesRegex(ValueError, '被跳过'):
                self.run_result([*self.complete(), event('skip', test)])

    def test_started_but_unfinished_test_is_not_success(self):
        with self.assertRaisesRegex(ValueError, '缺少执行通过'):
            self.run_result([event('run', 'TestPostgresHTTP'), event('pass')])

    def test_pass_without_execution_is_not_success(self):
        with self.assertRaisesRegex(ValueError, '缺少执行通过'):
            self.run_result([event('pass', 'TestPostgresHTTP'), event('pass')])

    def test_wrong_package_cannot_satisfy_required_test(self):
        events = [dict(item, Package='example.org/other') for item in self.complete()]
        with self.assertRaisesRegex(ValueError, '缺少执行通过'):
            self.run_result(events)

    def test_every_required_test_must_pass(self):
        events = [dict(event(action, test), Package='github.com/calm195/liyuanci-backend-go/internal/app')
                for action, test in [('run', 'TestPostgresLocalBusiness'), ('pass', 'TestPostgresLocalBusiness'), ('pass', '')]]
        with self.assertRaisesRegex(ValueError, 'TestPostgresWhisperRuntime'):
            self.run_result(events, suite='runtime')

    def test_failed_process_or_event_is_not_success(self):
        with self.assertRaisesRegex(ValueError, '原生集成失败'):
            self.run_result(self.complete(), code=1)
        with self.assertRaisesRegex(ValueError, '原生集成失败'):
            self.run_result([*self.complete(), event('fail')])

    def test_malformed_result_is_not_success(self):
        for raw in ('broken json\n', 'null\n', '[]\n'):
            with self.subTest(raw=raw), self.assertRaisesRegex(ValueError, '事件无效'):
                self.run_result(self.complete(), raw=raw)

    def test_missing_package_completion_is_not_success(self):
        with self.assertRaisesRegex(ValueError, '缺少测试包完成'):
            self.run_result(self.complete()[: - 1])


if __name__ == '__main__':
    unittest.main()
