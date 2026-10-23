"""宿主开发边界回归：凭据派生、最小依赖、项目归属和停止保护。"""
import argparse
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

import dev
import localdev
from services import OperationError


class LocalDevelopmentTest(unittest.TestCase):
    def test_host_config_preserves_credentials_and_shares_objects(self):
        with tempfile.TemporaryDirectory() as directory:
            entry = Path(directory) / 'operations.env'
            entry.write_text('LIYUANCI_LOCAL_PG_PORT=25432\n')
            settings = {field: 'host=postgres user=dev_api password=' + 'a' * 48 + ' dbname=liyuanci_business sslmode=disable'
                        for field in ('StudentDSN', 'ReviewDSN', 'WishDSN', 'AdminDSN', 'WorkerDSN')}
            settings.update(WrapKey='synthetic-key', AdminPassword='synthetic-password', ObjectDirectory='/container/objects',
                            CrisisDSN='host=postgres user=dev_crisis_api password=' + 'b' * 48 + ' dbname=liyuanci_business sslmode=disable')
            source = Path(directory) / 'business.json'
            source.write_text(json.dumps(settings))
            before = source.read_bytes()
            with patch.dict(os.environ, {'APP_ENVIRONMENT': 'production'}, clear=True):
                env = localdev.environment(entry)
            result = localdev.app_environment(entry, env)
            self.assertEqual(result['APP_ENVIRONMENT'], 'integration')
            self.assertEqual(result['APP_ROCKETMQ_ENABLED'], 'false')
            derived = json.loads(Path(result['APP_DEVAPPFILE']).read_text())
            self.assertIn('host=127.0.0.1 port=25432', derived['WorkerDSN'])
            self.assertEqual(derived['CrisisDSN'], settings['CrisisDSN'].replace('host=postgres ', 'host=127.0.0.1 port=25432 '))
            self.assertEqual(derived['WrapKey'], settings['WrapKey'])
            self.assertEqual(derived['AdminPassword'], settings['AdminPassword'])
            self.assertTrue(Path(derived['ObjectDirectory']).is_dir())
            self.assertEqual(source.read_bytes(), before)
            self.assertEqual(Path(result['APP_DEVAPPFILE']).stat().st_mode & 0o777, 0o600)
            self.assertNotIn('APP_CARD_MQFILE', result)
            self.assertNotIn('APP_CONTENT_MQFILE', result)
            self.assertNotIn('APP_CONTENT_ANALYZER', result)
            mq_file = Path(directory) / 'card-mq.yaml'
            mq_file.write_text('mq: {}\n')
            selected = localdev.app_environment(entry, dict(env, LIYUANCI_LOCAL_CARD_MQ_FILE=str(mq_file)))
            self.assertEqual(selected['APP_CARD_MQFILE'], str(mq_file.resolve()))
            self.assertEqual(selected['APP_ROCKETMQ_ENABLED'], 'false')
            selected = localdev.app_environment(entry, dict(env, LIYUANCI_LOCAL_CONTENT_MQ_FILE=str(mq_file), LIYUANCI_LOCAL_CONTENT_ANALYZER='synthetic'))
            self.assertEqual(selected['APP_CONTENT_MQFILE'], str(mq_file.resolve()))
            self.assertEqual(selected['APP_CONTENT_ANALYZER'], 'synthetic')
            self.assertNotIn('APP_CARD_MQFILE', selected)
            # 已有配置未启用危机后台时，不隐式生成身份或连接。
            del settings['CrisisDSN']
            source.write_text(json.dumps(settings))
            self.assertNotIn('CrisisDSN', json.loads(localdev.prepare(entry, env).read_text()))

    def test_privacy_connections_keep_clusters_and_credentials_separate(self):
        with tempfile.TemporaryDirectory() as directory:
            entry = Path(directory) / 'operations.env'
            entry.write_text('')
            import privacydev
            cfg = {field: 'host=postgres user=dev_api password=' + 'a' * 48 + ' dbname=liyuanci_business sslmode=disable'
                   for field in ('StudentDSN', 'ReviewDSN', 'WishDSN', 'AdminDSN', 'WorkerDSN')}
            cfg['WrapKey'] = 'historical-wrap'
            privacydev.complete(cfg)
            before = cfg.copy()
            privacydev.complete(cfg)
            self.assertEqual(cfg, before)
            self.assertNotEqual(cfg['RecoveryWriterDSN'], cfg['RecoveryReaderDSN'])
            source = Path(directory) / 'business.json'
            source.write_text(json.dumps(cfg))
            derived = json.loads(localdev.prepare(entry, {'LIYUANCI_LOCAL_RECOVERY_PG_PORT': '25433'}).read_text())
            for field in privacydev.BUSINESS_ROLES:
                self.assertIn('port=15432', derived[field])
            for field in privacydev.RECOVERY_ROLES:
                self.assertIn('port=25433', derived[field])
                self.assertIn('dbname=liyuanci_recovery', derived[field])
            self.assertEqual(derived['RecoveryWrapKey'], cfg['RecoveryWrapKey'])
            self.assertEqual(json.loads(source.read_text()), before)
            with self.assertRaises(OperationError):
                localdev.recovery_host_dsn(cfg['PrivacyDSN'], {})

    def test_existing_recovery_database_missing_keys_refuses_replacement(self):
        import privacydev
        with tempfile.TemporaryDirectory() as directory:
            entry = Path(directory) / 'operations.env'
            entry.write_text('')
            manifest = Path(directory) / 'business.json'
            manifest.write_text('{"WrapKey":"historical"}')
            original = manifest.read_bytes()
            def run(command, stage, data=None):
                return 't' if data and 'SELECT EXISTS' in data else ''
            with self.assertRaises(OperationError):
                privacydev.initialize({'WrapKey': 'historical'}, manifest, entry, {}, [], run, unittest.mock.Mock())
            self.assertEqual(manifest.read_bytes(), original)
            self.assertFalse(manifest.with_name('recovery-migration.json').exists())

    def test_native_privacy_init_refuses_docker_side_effects(self):
        with patch.object(dev.subprocess, 'run') as run:
            with self.assertRaises(OperationError):
                dev.main(['privacy-init', '--native'])
            run.assert_not_called()

    def test_native_defaults_and_password_file(self):
        with tempfile.TemporaryDirectory() as directory:
            entry = Path(directory) / 'operations.env'
            entry.write_text('LIYUANCI_LOCAL_MIDDLEWARE=native\n')
            cfg = {field: 'host=postgres user=dev_api password=' + 'a' * 48 + ' dbname=liyuanci_business sslmode=disable'
                   for field in ('StudentDSN', 'ReviewDSN', 'WishDSN', 'AdminDSN', 'WorkerDSN')}
            cfg['CrisisDSN'] = 'host=postgres user=dev_crisis_api password=' + 'b' * 48 + ' dbname=liyuanci_business sslmode=disable'
            (Path(directory) / 'business.json').write_text(json.dumps(cfg))
            password = Path(directory) / 'redis-password'
            password.write_text('synthetic-redis-password')
            env = {'LIYUANCI_LOCAL_MIDDLEWARE': 'native', 'LIYUANCI_NATIVE_REDIS_PASSWORD_FILE': str(password)}
            with patch.dict(os.environ, {'PGPASSWORD': 'unrelated-admin-password'}, clear=True):
                self.assertNotIn('PGPASSWORD', localdev.environment(entry))
            result = localdev.app_environment(entry, env)
            self.assertEqual(result['APP_REDIS_ADDRESS'], '127.0.0.1:6379')
            self.assertEqual(result['APP_REDIS_PASSWORD'], 'synthetic-redis-password')
            self.assertIn('port=5432', json.loads(Path(result['APP_DEVAPPFILE']).read_text())['WishDSN'])
            self.assertIn('port=5432', json.loads(Path(result['APP_DEVAPPFILE']).read_text())['CrisisDSN'])

    def test_native_infra_never_calls_docker_without_mq(self):
        args = argparse.Namespace(project='liyuanci-local-native', management=False, monitoring=False, mq=False)
        with patch.object(dev.socket, 'create_connection') as connect, patch.object(dev, 'service_manager') as manager:
            dev.infrastructure(args, {'LIYUANCI_LOCAL_MIDDLEWARE': 'native'})
            self.assertEqual(connect.call_count, 2)
            manager.assert_not_called()

    def test_native_mq_starts_no_database_or_cache(self):
        with tempfile.TemporaryDirectory() as directory:
            args = argparse.Namespace(project='liyuanci-local-native', env_file=str(Path(directory) / 'operations.env'),
                                      management=False, monitoring=False, mq=True)
            manager = unittest.mock.Mock()
            with patch.object(dev.socket, 'create_connection'), patch.object(dev, 'service_manager', return_value=manager):
                dev.infrastructure(args, {'LIYUANCI_LOCAL_MIDDLEWARE': 'native'})
            calls = [call.args[0] for call in manager.call.call_args_list]
            self.assertEqual(calls[-1][-2:], ['namesrv', 'broker'])
            self.assertFalse({'postgres', 'redis', 'api', 'worker'} & set(sum(calls, [])))
            self.assertTrue((Path(directory) / 'native-mq.started').exists())

    def test_native_stop_does_not_require_docker(self):
        with tempfile.TemporaryDirectory() as directory:
            entry = Path(directory) / 'operations.env'
            entry.write_text('LIYUANCI_LOCAL_MIDDLEWARE=native\nLIYUANCI_LOCAL_PROJECT=liyuanci-local-native\n')
            with patch.object(dev.subprocess, 'run') as run:
                dev.main(['infra-stop', '--native', '--env-file', str(entry)])
                run.assert_not_called()
            with self.assertRaises(OperationError):
                dev.main(['infra', '--env-file', str(entry), '--project', 'liyuanci-local-native'])

    def test_invalid_dsn_and_ports_are_rejected(self):
        for dsn in ('host=production password=private', 'postgres://private'):
            with self.assertRaises(OperationError):
                localdev.host_dsn(dsn, {})
        for value in ('0', '65536', '5432 user=postgres'):
            with self.assertRaises(OperationError):
                localdev.port({'LIYUANCI_LOCAL_PG_PORT': value}, 'PG', 15432)

    def test_minimum_infrastructure_never_starts_apps_or_mq(self):
        args = argparse.Namespace(project='liyuanci-local-test', management=False, monitoring=False, mq=False)
        manager = unittest.mock.Mock()
        with tempfile.NamedTemporaryFile(mode='w') as secret:
            secret.write('synthetic-password')
            secret.flush()
            manager.env = {}
            manager.secret_file.return_value = Path(secret.name)
            with patch.object(dev, 'service_manager', return_value=manager):
                dev.infrastructure(args, {})
        calls = [call.args[0] for call in manager.call.call_args_list]
        self.assertEqual(calls[-1][-2:], ['postgres', 'redis'])
        self.assertFalse({'api', 'worker', 'broker', 'namesrv', '--build', 'migrate'} & set(sum(calls, [])))

    def test_production_and_wrong_project_refused_before_commands(self):
        with patch.object(dev.subprocess, 'run') as run:
            with self.assertRaises(OperationError):
                dev.main(['infra', '--project', 'liyuanci-production'])
            run.assert_not_called()
        with tempfile.TemporaryDirectory() as directory:
            entry = Path(directory) / 'operations.env'
            entry.write_text('LIYUANCI_LOCAL_PROJECT=liyuanci-local-other\n')
            with self.assertRaises(OperationError):
                dev.main(['infra-stop', '--env-file', str(entry)])

    def test_stop_cleans_exited_pid_without_signalling(self):
        with tempfile.TemporaryDirectory() as directory:
            entry = Path(directory) / 'operations.env'
            pidfile = Path(directory) / 'server.pid'
            pidfile.write_text('123')
            exited = subprocess.CompletedProcess([], 1, '', '')
            with patch.object(dev.subprocess, 'run', return_value=exited), patch.object(dev.os, 'kill') as kill:
                dev.stop_apps(entry)
                kill.assert_not_called()
            self.assertFalse(pidfile.exists())

    def test_stop_never_signals_reused_or_unrelated_pid(self):
        with tempfile.TemporaryDirectory() as directory:
            entry = Path(directory) / 'operations.env'
            (Path(directory) / 'server.pid').write_text('123')
            unrelated = subprocess.CompletedProcess([], 0, '/another/server -config secret', '')
            with patch.object(dev.subprocess, 'run', return_value=unrelated), patch.object(dev.os, 'kill') as kill:
                with self.assertRaises(OperationError):
                    dev.stop_apps(entry)
                kill.assert_not_called()


    def test_stop_tracks_content_worker_and_protects_unrelated_pid(self):
        with tempfile.TemporaryDirectory() as directory:
            entry = Path(directory) / 'operations.env'
            pidfile = Path(directory) / 'content-worker.pid'
            pidfile.write_text('123')
            unrelated = subprocess.CompletedProcess([], 0, '/another/content-worker', '')
            with patch.object(dev.subprocess, 'run', return_value=unrelated), patch.object(dev.os, 'kill') as kill:
                with self.assertRaises(OperationError):
                    dev.stop_apps(entry)
                kill.assert_not_called()
            exited = subprocess.CompletedProcess([], 1, '', '')
            with patch.object(dev.subprocess, 'run', return_value=exited):
                dev.stop_apps(entry)
            self.assertFalse(pidfile.exists())


if __name__ == '__main__':
    unittest.main()
