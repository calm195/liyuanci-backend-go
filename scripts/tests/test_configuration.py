"""私有目录迁移与版本控制边界，验证原凭据/对象保留及导入失败不发布目标。"""
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

import configuration
import services


class ConfigurationTest(unittest.TestCase):
    def test_private_paths_only_allow_ignored_subtree_inside_repository(self):
        self.assertEqual(configuration.private_path(configuration.ROOT / '.local/native/key'),
                         configuration.ROOT / '.local/native/key')
        with self.assertRaises(ValueError):
            configuration.private_path(configuration.ROOT / 'configs/key')
        result = subprocess.run(['git', 'check-ignore', '-q', '.local/native/key'], cwd=configuration.ROOT)
        self.assertEqual(result.returncode, 0)

    def test_import_keeps_keys_objects_and_originals_and_never_overwrites(self):
        with tempfile.TemporaryDirectory() as folder:
            home = Path(folder).resolve()
            source = home / '.config/liyuanci/native'
            source.mkdir(parents=True)
            (source / 'business.json').write_text('synthetic-key')
            (source / 'objects').mkdir()
            (source / 'objects/ciphertext').write_bytes(b'synthetic-ciphertext')
            (source / 'operations.env').write_text('LIYUANCI_LOCAL_MIDDLEWARE=native\nLIYUANCI_DEV_BUSINESS_FILE=' + str(source / 'business.json') + '\n')
            target = home / 'private'
            with patch.object(configuration, 'LOCAL_ROOT', target), patch.object(configuration.Path, 'home', return_value=home):
                self.assertTrue(configuration.import_legacy('native'))
                imported = services.load_env(target / 'native/operations.env', {})
                self.assertEqual(imported['LIYUANCI_DEV_BUSINESS_FILE'], str(target / 'native/business.json'))
                self.assertEqual((target / 'native/objects/ciphertext').read_bytes(), b'synthetic-ciphertext')
                (source / 'business.json').write_text('new-value-must-not-overwrite')
                self.assertFalse(configuration.import_legacy('native'))
                self.assertEqual((target / 'native/business.json').read_text(), 'synthetic-key')
                self.assertTrue(source.exists())

    def test_import_refuses_running_old_application(self):
        with tempfile.TemporaryDirectory() as folder:
            home = Path(folder).resolve()
            source = home / '.config/liyuanci/local'
            source.mkdir(parents=True)
            (source / 'operations.env').write_text('')
            (source / 'server.pid').write_text('123')
            with patch.object(configuration, 'LOCAL_ROOT', home / 'private'), patch.object(configuration.Path, 'home', return_value=home), patch.object(configuration.os, 'kill'):
                with self.assertRaises(ValueError):
                    configuration.import_legacy('local')
                self.assertFalse((home / 'private/local').exists())

    def test_missing_original_reference_does_not_publish_partial_import(self):
        with tempfile.TemporaryDirectory() as folder:
            home = Path(folder).resolve()
            source = home / '.config/liyuanci/local'
            source.mkdir(parents=True)
            (source / 'operations.env').write_text('LIYUANCI_TEST_DB_PASSWORD_FILE=missing\n')
            target = home / 'private'
            with patch.object(configuration, 'LOCAL_ROOT', target), patch.object(configuration.Path, 'home', return_value=home):
                with self.assertRaises(ValueError):
                    configuration.import_legacy('local')
                self.assertFalse((target / 'local').exists())
                self.assertFalse(list(target.iterdir()))


if __name__ == '__main__':
    unittest.main()
