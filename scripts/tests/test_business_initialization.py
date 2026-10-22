"""显式初始化增量用途密钥保护，不调用数据库或运行迁移。"""
import base64
import importlib.util
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location('business_init', Path(__file__).resolve().parents[1] / 'init-business-dev.py')
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


class BusinessInitializationTest(unittest.TestCase):
    def test_upgrade_preserves_historical_keys_and_worker_identity(self):
        cfg = {'WrapKey': 'historical-wrap', 'LookupKey': 'historical-lookup', 'AdminPassword': 'historical-password', 'WorkerDSN': 'historical-worker'}
        before = cfg.copy()
        self.assertTrue(module.complete_settings(cfg))
        self.assertEqual(len(base64.b64decode(cfg['ModerationKey'])), 32)
        self.assertEqual({key: cfg[key] for key in before}, before)
        saved = cfg.copy()
        self.assertFalse(module.complete_settings(cfg))
        self.assertEqual(cfg, saved)

    def test_upgrade_adds_missing_worker_without_replacing_moderation_key(self):
        cfg = {'ModerationKey': 'historical-moderation'}
        self.assertTrue(module.complete_settings(cfg))
        self.assertEqual(cfg['ModerationKey'], 'historical-moderation')
        self.assertIn('user=dev_domain_worker', cfg['WorkerDSN'])
