#!/usr/bin/env python3
"""显式初始化本地单DSN业务联调，复用历史密钥与账号；不迁移、不重置、不启动应用。"""
import argparse
import base64
import hashlib
import hmac
import json
import os
from pathlib import Path
import re
import secrets
import shutil
import subprocess
import time

from configuration import private_path
from localdev import app_environment, environment, native_database_environment
from services import ROOT, OperationError


def save(path, value):
    """私有文件原子落盘，不回显凭据。"""
    pending = path.with_name(path.name + '.pending')
    pending.write_text(json.dumps(value, ensure_ascii=False, indent=2) + '\n')
    pending.chmod(0o600)
    pending.replace(path)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--env-file', required=True)
    parser.add_argument('--project', required=True)
    parser.add_argument('--native', action='store_true')
    args = parser.parse_args()
    if not re.fullmatch(r'liyuanci-local(?:-[a-z0-9_-]+)?', args.project):
        raise OperationError('只允许本地隔离项目')
    entry = private_path(args.env_file)
    state = entry.parent
    env = environment(entry)
    if env.get('LIYUANCI_LOCAL_PROJECT') != args.project:
        raise OperationError('项目归属不匹配')
    # 所选应用停止后才切换配置，拒绝边运行边替换密钥和日志来源。
    for pidfile in state.glob('*.pid'):
        pid = pidfile.read_text().strip()
        status = subprocess.run(['ps', '-p', pid, '-o', 'stat='], capture_output=True, text=True)
        if status.returncode == 0 and not status.stdout.strip().startswith('Z'):
            raise OperationError('请先运行对应dev-stop，再显式初始化测试模式')
    source = private_path(env.get('LIYUANCI_DEV_BUSINESS_FILE', state / 'business.json'))
    original = json.loads(source.read_text())
    target = state / 'business.simple.json'
    whisper_file = state / 'whispers-local.json'
    if target.exists() != whisper_file.exists():
        raise OperationError('本地配置不完整，请恢复成套历史文件，不生成替代密钥')
    backup = state / 'backups' / 'before-simple-business'
    if not backup.exists():
        backup.mkdir(parents=True, mode=0o700)
        for path in (source, entry):
            shutil.copy2(path, backup / path.name)
            (backup / path.name).chmod(0o600)

    def sql(statement, database='liyuanci_business', recovery=False):
        sql_env = env.copy()
        if args.native:
            sql_env = native_database_environment(env)
            command = ['psql', '-X', '-w', '-d', database, '-v', 'ON_ERROR_STOP=1', '-At']
        else:
            container = args.project + ('-recovery-postgres-1' if recovery else '-postgres-1')
            command = ['docker', 'exec', '-i', container, 'psql', '-U', 'liyuanci', '-d', database, '-v', 'ON_ERROR_STOP=1', '-At']
        result = subprocess.run(command, input=statement, text=True, capture_output=True, env=sql_env)
        if result.returncode:
            raise OperationError('数据库初始化检查失败；未输出可能含凭据的底层结果')
        return result.stdout.strip()

    if not args.native and original.get('RecoveryWriterDSN'):
        # 不能将旧恢复意图留在另一资源后静默换为空日志。
        if sql('SELECT count(*) FROM public.recovery_delete_journal;', 'liyuanci_recovery', True) != '0':
            raise OperationError('旧恢复日志非空，请先显式迁移历史意图；原配置保持可用')
    if sql("SELECT max(version)>=26 FROM public.schema_migrations;") != 't':
        raise OperationError('请先显式应用0026与permissions')
    if sql('SELECT opaque_enabled FROM public.interaction_block_policy WHERE singleton;') != 't':
        raise OperationError('请先完成客户端升级及显式opaque切换')

    if target.exists():
        cfg = json.loads(target.read_text())
    else:
        cfg = {k: v for k, v in original.items() if not k.endswith('DSN')}
        cfg.update(LocalTest=True, DatabaseDSN='host=postgres user=dev_local_business password=' + secrets.token_hex(24) + ' dbname=liyuanci_business sslmode=disable', JournalFile=str(state / 'local-business-journal.json'))
        cfg.setdefault('RecoveryWrapKey', base64.b64encode(secrets.token_bytes(32)).decode())
    match = re.fullmatch(r'host=postgres user=dev_local_business password=([0-9a-f]{48}) dbname=liyuanci_business sslmode=disable', cfg['DatabaseDSN'])
    if not match:
        raise OperationError('共用连接格式不匹配，拒绝修改身份')
    sql("DO $$ BEGIN IF NOT EXISTS(SELECT 1 FROM pg_roles WHERE rolname='dev_local_business') THEN CREATE ROLE dev_local_business LOGIN PASSWORD '" + match[1] + "' NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS; END IF; END $$;\nGRANT liyuanci_api,liyuanci_identity_maintenance,liyuanci_admin_api,liyuanci_admin_maintenance,liyuanci_domain_worker,liyuanci_crisis_api,liyuanci_privacy_maintenance,liyuanci_evidence_delete_api,liyuanci_evidence_purger TO dev_local_business;\n")
    save(target, cfg)
    if whisper_file.exists():
        tree = json.loads(whisper_file.read_text())
    else:
        tree = json.loads((ROOT / 'configs/examples/whispers-runtime.json').read_text())
        for key in list(tree):
            if key.endswith('DSN') or key in ('GatewayURL', 'GatewayTokenFile', 'GatewayCAFile', 'ProviderVersion', 'KeyRefs', 'RecoveryKeyRefs', 'CatalogFile'):
                del tree[key]
        tree.update(LocalTest=True, Enabled=True, DatabasePolling=True, DatabaseDSN=cfg['DatabaseDSN'], JournalFile=cfg['JournalFile'], WrapKey=cfg['WrapKey'], RecoveryWrapKey=cfg['RecoveryWrapKey'], Contact='')
        for name in ('ReceiptKey', 'CursorKey', 'RateKey', 'GovernanceKey', 'PrivacyKey', 'EvidenceKey', 'SafetyKey'):
            tree[name] = base64.b64encode(secrets.token_bytes(32)).decode()
        # 与宿主旧注销及安全服务共用同一用途，不使既有回执失效。
        root_key = base64.b64decode(cfg['WishKey'])
        tree['PrivacyKey'] = base64.b64encode(hmac.new(root_key, b'privacy.receipt.v1', hashlib.sha256).digest()).decode()
        tree['SafetyKey'] = base64.b64encode(hmac.new(root_key, b'safety.scope.v1', hashlib.sha256).digest()).decode()
    # 派生宿主地址不覆盖唯一业务凭据，所有进程读取同一份树洞配置。
    host_env = dict(env, LIYUANCI_LOCAL_SIMPLE_BUSINESS_FILE=str(target))
    app_env = app_environment(entry, host_env)
    tree['DatabaseDSN'] = json.loads(Path(app_env['APP_DEVAPPFILE']).read_text())['DatabaseDSN']
    save(whisper_file, tree)
    result = subprocess.run(['go', 'run', './cmd/dev-init'], cwd=ROOT, env=dict(app_env, APP_ADMIN_BOOTSTRAP_LOGIN='developer'), capture_output=True, text=True)
    if result.returncode:
        raise OperationError('共用身份装配检查失败，原入口配置未切换')
    updates = {'LIYUANCI_LOCAL_SIMPLE_BUSINESS_FILE': str(target), 'LIYUANCI_LOCAL_WHISPERS_FILE': str(whisper_file)}
    lines = [line for line in entry.read_text().splitlines() if line.split('=', 1)[0] not in updates]
    entry.write_text('\n'.join(lines) + '\n' + ''.join(k + '=' + v + '\n' for k, v in updates.items()))
    entry.chmod(0o600)
    print('本地业务测试配置完成：单DSN、历史密钥复用、树洞本机适配、数据库轮询；联系方式未配置，不代表正式外部能力验收。')


if __name__ == '__main__':
    started = time.monotonic()
    result = 'failure'
    try:
        main()
        result = 'success'
    except (OperationError, OSError, ValueError, KeyError, subprocess.SubprocessError) as error:
        print(str(error) if isinstance(error, OperationError) else '本地测试初始化失败，原数据与凭据保留')
        raise SystemExit(1)
    finally:
        print(json.dumps({'msg': '本地业务测试初始化完成', 'result': result, 'duration': round(time.monotonic() - started, 3)}, ensure_ascii=False))
