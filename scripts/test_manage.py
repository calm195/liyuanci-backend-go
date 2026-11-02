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
        (self.root/'common.yaml').write_text('mode: development\nserver:\n  address: 127.0.0.1:8080\ndatabase:\n  maxopen: 6\n')
        self.path = self.root/'development.yaml'

    def test_layered_merge_and_literal_secret(self):
        self.path.write_text('database:\n  password: "literal-$TOKEN"\nserver:\n  address: 127.0.0.1:8090\n')
        with patch.dict('os.environ', {'APP_DATABASE_PASSWORD': 'wrong'}):
            value = settings.layers(self.path)
        self.assertEqual(value['database'], {'maxopen': 6, 'password': 'literal-$TOKEN'})
        self.assertEqual(value['server']['address'], '127.0.0.1:8090')

    def test_duplicate_keys_and_null_rejected(self):
        for raw in ('database:\n  password: a\n  PASSWORD: b\n', 'database: null\n', 'a: &key x\nb: *key\n'):
            self.path.write_text(raw)
            with self.assertRaises(ValueError): settings.layers(self.path)

    def test_initialize_never_replaces_existing_keys(self):
        manage.initialize(self.path, 'development')
        original = self.path.read_bytes()
        self.assertEqual(self.path.stat().st_mode & 0o777, 0o600)
        with self.assertRaises(ValueError): manage.initialize(self.path, 'development')
        self.assertEqual(self.path.read_bytes(), original)
        values = settings.read(self.path)
        self.assertEqual(len(base64.b64decode(values['masterkey'], validate=True)), 32)
        self.assertFalse(any(key.endswith('key') for key in values['development']))
        self.assertIn('唯一应用主密钥', original.decode())
        self.assertEqual(values['database']['password'], '')
        self.assertNotIn('dsn', values['database'])
        self.assertEqual(values['database']['port'], 5432)

    def test_worker_uses_same_environment_configuration(self):
        self.path.write_text('environment: development\n')
        with patch.object(Path, 'is_file', return_value=True):
            for kind in ('card','content','whispers','account','evidence'):
                cmd=manage.command('worker',self.path,kind)
                self.assertEqual(cmd[1:], ['-config',str(self.path),'-kind',kind])
            self.path.write_text('mode: business\nenvironment: production\n')
            cmd=manage.command('worker',self.path,'evidence')
            self.assertEqual(Path(cmd[0]).name,'business-worker')


if __name__ == '__main__': unittest.main()
