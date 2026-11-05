"""简化原生入口的配置覆盖、初始化及进程选择回归。"""
import base64
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch
import manage
import yaml_settings as settings


class ConfigurationTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        (self.root / 'common.yaml').write_text('mode: development\nserver:\n  address: 127.0.0.1:8080\ndatabase:\n  maxopen: 6\n')
        self.path = self.root / 'development.yaml'

    def test_layered_merge_and_literal_secret(self):
        self.path.write_text('database:\n  password: "literal-$TOKEN"\nserver:\n  address: 127.0.0.1:8090\n')
        with patch.dict('os.environ', {'APP_DATABASE_PASSWORD': 'wrong'}):
            value = settings.layers(self.path)
        self.assertEqual(value['database'], {'maxopen': 6, 'password': 'literal-$TOKEN'})
        self.assertEqual(value['server']['address'], '127.0.0.1:8090')

    def test_duplicate_keys_and_null_rejected(self):
        for raw in ('database:\n  password: a\n  PASSWORD: b\n', 'database: null\n', 'a: &key x\nb: *key\n'):
            self.path.write_text(raw)
            with self.assertRaises(ValueError):
                settings.layers(self.path)

    def test_reject_removed_development_block(self):
        self.path.write_text('development:\n  adminpassword: obsolete\n')
        with self.assertRaises(ValueError):
            settings.layers(self.path)

    def test_reject_retired_runtime_fields(self):
        for block in ('business:\n  runtime:\n', 'whispers:\n'):
            indent = '    ' if block.startswith('business:') else '  '
            for field in ('gatewayurl', 'gatewaytoken', 'gatewayca', 'providerversion',
                          'catalogfile', 'worker', 'interval', 'operationtimeout', 'contact', 'care'):
                with self.subTest(block=block, field=field):
                    self.path.write_text(block + indent + field + ": ''\n")
                    with self.assertRaises(ValueError):
                        settings.layers(self.path)
        self.path.write_text('worker:\n  contentanalyzer: manual\n')
        with self.assertRaises(ValueError):
            settings.layers(self.path)

    def test_tasks_merge_preserves_common_budget(self):
        with (self.root / 'common.yaml').open('a') as output:
            output.write('business:\n  runtime:\n    resourcesfile: resources.yaml\n    tasks:\n      scaninterval: 1s\n      operationtimeout: 1m\n')
        self.path.write_text('business:\n  runtime:\n    tasks:\n      scaninterval: 2s\n')
        runtime = settings.layers(self.path)['business']['runtime']
        self.assertEqual(runtime['resourcesfile'], 'resources.yaml')
        self.assertEqual(runtime['tasks'], {'scaninterval': '2s', 'operationtimeout': '1m'})

    def test_initialize_never_replaces_existing_keys(self):
        manage.initialize(self.path, 'development')
        original = self.path.read_bytes()
        self.assertEqual(self.path.stat().st_mode & 0o777, 0o600)
        with self.assertRaises(ValueError):
            manage.initialize(self.path, 'development')
        self.assertEqual(self.path.read_bytes(), original)
        values = settings.read(self.path)
        self.assertEqual(len(base64.b64decode(values['masterkey'], validate=True)), 32)
        self.assertEqual(len(values['business']['adminpassword']), 32)
        self.assertEqual(len(values['business']['admincreatepassword']), 32)
        self.assertNotEqual(values['business']['adminpassword'], values['business']['admincreatepassword'])
        self.assertNotIn('development', values)
        self.assertFalse(any(key.endswith('key') for key in values['business']))
        self.assertIn('唯一应用主密钥', original.decode())
        self.assertEqual(values['database']['password'], '')
        self.assertNotIn('dsn', values['database'])
        self.assertEqual(values['database']['port'], 5432)

    def test_worker_uses_same_environment_configuration(self):
        self.path.write_text('environment: development\n')
        with patch.object(Path, 'is_file', return_value=True):
            for kind in ('card', 'content', 'whispers', 'account', 'evidence'):
                cmd = manage.command('worker', self.path, kind)
                self.assertEqual(Path(cmd[0]).name, 'worker')
                self.assertEqual(cmd[1:], ['-config', str(self.path), '-kind', kind])
            self.path.write_text('mode: business\nenvironment: production\n')
            cmd = manage.command('worker', self.path, 'evidence')
            self.assertEqual(Path(cmd[0]).name, 'worker')

    def test_unified_init_and_execution_modes(self):
        self.path.write_text('environment: development\n')
        with patch.object(Path, 'is_file', return_value=True):
            for mode in ('development', 'business', 'whispers'):
                self.path.write_text('mode: ' + mode + '\n')
                cmd = manage.command('init', self.path, 'content', check_config=True)
                self.assertEqual(Path(cmd[0]).name, 'init')
                self.assertEqual(cmd[-1], '-check-config')
            for key, flag in (('check_config', '-check-config'), ('check', '-check'), ('recover', '-recover')):
                cmd = manage.command('worker', self.path, 'account', **{key: True})
                self.assertEqual(Path(cmd[0]).name, 'worker')
                self.assertEqual(cmd[-1], flag)
            for action, kind, options in (
                ('worker', 'card', {'recover': True}),
                ('init', 'account', {'check': True}),
                ('worker', 'account', {'check': True, 'recover': True}),
                ('worker', 'unknown', {}),
                ('migrate', 'account', {'check_config': True}),
            ):
                with self.subTest(action=action, kind=kind, options=options), self.assertRaises(ValueError):
                    manage.command(action, self.path, kind, **options)


if __name__ == '__main__':
    unittest.main()
