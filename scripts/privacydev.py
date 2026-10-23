"""显式初始化本地隐私身份与独立恢复集群；保留历史凭据，不提供模拟清理器。"""
import base64
import json
from pathlib import Path
import re
import secrets

from localdev import port, recovery_host_dsn
from services import OperationError


BUSINESS_ROLES = {'PrivacyDSN': 'privacy_maintenance',
                  'EvidenceDeletionDSN': 'evidence_delete_api',
                  'EvidencePurgerDSN': 'evidence_purger'}
RECOVERY_ROLES = {'RecoveryWriterDSN': 'recovery_writer', 'RecoveryReaderDSN': 'recovery_reader'}


def complete(cfg):
    """只在显式初始化补齐新用途，保留所有历史密钥、身份与口令。"""
    cfg.setdefault('RecoveryWrapKey', base64.b64encode(secrets.token_bytes(32)).decode())
    for fields, host, database in ((BUSINESS_ROLES, 'postgres', 'liyuanci_business'),
                                   (RECOVERY_ROLES, 'recovery-postgres', 'liyuanci_recovery')):
        for field, role in fields.items():
            cfg.setdefault(field, f'host={host} user=dev_{role} password={secrets.token_hex(24)} dbname={database} sslmode=disable')


def save(path, data, mode=0o600):
    """在0700私有目录原子落盘，迁移凭据额外限制为0600。"""
    pending = Path(str(path) + '.pending')
    with pending.open('w') as output:
        json.dump(data, output, indent=2)
    pending.chmod(mode)
    pending.replace(path)


def initialize(cfg, manifest, entry, env, base, run, sql):
    """只由privacy-init调用，普通启动不创建角色、数据库或替换密钥。"""
    port(env, 'RECOVERY_PG', 15433)
    run(base + ['up', '-d', '--wait', 'recovery-postgres'], '启动独立恢复日志数据库')

    def recovery_sql(statement, database='postgres'):
        return run(base + ['exec', '-T', 'recovery-postgres', 'psql', '-U', 'liyuanci', '-d', database,
                           '-v', 'ON_ERROR_STOP=1', '-At'], '显式初始化恢复日志身份', statement)

    exists = recovery_sql("SELECT EXISTS(SELECT 1 FROM pg_database WHERE datname='liyuanci_recovery');\n").strip() == 't'
    migration_file = manifest.with_name('recovery-migration.json')
    if exists and (not cfg.get('RecoveryWrapKey') or not migration_file.exists() or
                   any(not cfg.get(field) for field in (*BUSINESS_ROLES, *RECOVERY_ROLES))):
        raise OperationError('已有恢复数据库但缺历史凭据或密钥，请恢复原文件，禁止生成替代配置')
    complete(cfg)
    # 延续初始化器的容器非root只读挂载约定，宿主父目录必须为0700。
    save(manifest, cfg, 0o644)
    if not migration_file.exists():
        save(migration_file, {'DSN': 'host=recovery-postgres user=dev_recovery_migrator password=' +
                             secrets.token_hex(24) + ' dbname=liyuanci_recovery sslmode=disable'})
    migration_dsn = json.loads(migration_file.read_text())['DSN']

    def identity(dsn, role, host, database, execute):
        match = re.fullmatch(f'host={host} user=dev_{role} password=([0-9a-f]{{48}}) dbname={database} sslmode=disable', dsn)
        if not match:
            raise OperationError('隐私连接配置格式不匹配，拒绝初始化')
        group, login = 'liyuanci_' + role, 'dev_' + role
        execute("DO $$ BEGIN IF NOT EXISTS(SELECT 1 FROM pg_roles WHERE rolname='" + group + "') THEN CREATE ROLE " + group + " NOLOGIN; END IF; "
                "IF NOT EXISTS(SELECT 1 FROM pg_roles WHERE rolname='" + login + "') THEN CREATE ROLE " + login + " LOGIN PASSWORD '" + match[1] + "'; END IF; END $$;\n"
                "GRANT " + group + " TO " + login + ";\n")

    for field, role in BUSINESS_ROLES.items():
        identity(cfg[field], role, 'postgres', 'liyuanci_business', sql)
    for field, role in RECOVERY_ROLES.items():
        identity(cfg[field], role, 'recovery-postgres', 'liyuanci_recovery', recovery_sql)
    identity(migration_dsn, 'recovery_migrator', 'recovery-postgres', 'liyuanci_recovery', recovery_sql)
    if not exists:
        recovery_sql('CREATE DATABASE liyuanci_recovery OWNER dev_recovery_migrator;\n')
    env['APP_RECOVERY_DATABASE_DSN'] = recovery_host_dsn(migration_dsn, env)
    run(['go', 'run', './cmd/migrate', 'journal-up'], '专用身份显式迁移恢复日志')
    env['APP_RECOVERY_DATABASE_DSN'] = ('host=127.0.0.1 port=' + port(env, 'RECOVERY_PG', 15433) +
        ' user=liyuanci password=' + env['LIYUANCI_TEST_DB_PASSWORD'] + ' dbname=liyuanci_recovery sslmode=disable')
    run(['go', 'run', './cmd/migrate', 'journal-permissions'], '显式配置恢复日志权限')
    env.pop('APP_RECOVERY_DATABASE_DSN', None)
    if not any(line == 'LIYUANCI_LOCAL_PRIVACY_ENABLED=true' for line in entry.read_text().splitlines()):
        with entry.open('a') as output:
            output.write('\nLIYUANCI_LOCAL_PRIVACY_ENABLED=true\n')
