#!/usr/bin/env python3
"""显式初始化隔离开发业务库、窄权限身份、忽略目录内或显式外部密钥与首个后台账号；不重置已有账号。"""
import argparse
import base64
import json
import os
from pathlib import Path
import secrets
import subprocess
import time

from services import ROOT, OperationError, load_env
from configuration import directory, private_path


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--env-file', default=str(directory('dev') / 'operations.env'))
    parser.add_argument('--project', default='liyuanci-dev')
    parser.add_argument('--admin-login', required=True)
    parser.add_argument('--host', action='store_true', help='通过宿主Go命令迁移和初始化')
    parser.add_argument('--native', action='store_true', help='使用已经运行的宿主PG/Redis，不访问Docker')
    args = parser.parse_args()
    if args.native and not args.host:
        raise OperationError('--native必须与--host一起使用')
    # 与启停入口共用项目名校验，拒绝误用生产项目。
    import re
    if not re.fullmatch(r'[a-z0-9][a-z0-9_-]*', args.project) or 'production' in args.project:
        raise OperationError('业务初始化只允许隔离开发项目')
    entry = Path(args.env_file).expanduser().resolve()
    env = load_env(entry, dict(os.environ))
    private_path(entry)
    if args.native:
        from localdev import native_database_environment
        if env.get('LIYUANCI_LOCAL_MIDDLEWARE') != 'native':
            raise OperationError('原生初始化必须使用独立native配置')
        env = native_database_environment(env)
    else:
        password_file = Path(env['LIYUANCI_TEST_DB_PASSWORD_FILE'])
        private_path(password_file)
        env['LIYUANCI_TEST_DB_PASSWORD'] = password_file.read_text().strip()
    manifest = Path(env.get('LIYUANCI_DEV_BUSINESS_FILE', str(entry.parent / 'business.json'))).resolve()
    if '\n' in str(manifest) or '\r' in str(manifest):
        raise OperationError('业务配置路径不能包含换行')
    private_path(manifest)
    base = ['docker', 'compose', '-p', args.project, '-f', str(ROOT / 'deploy/compose/integration.yaml')]

    if args.host:
        base += ['-f', str(ROOT / 'deploy/compose/host-dev.yaml')]

    def run(argv, stage, data=None):
        started = time.monotonic()
        result = subprocess.run(argv, cwd=ROOT, env=env, input=data, text=True, capture_output=True, timeout=900)
        print(json.dumps({'msg': '开发业务初始化阶段完成', 'stage': stage,
                          'result': 'success' if result.returncode == 0 else 'failure',
                          'duration': round(time.monotonic()-started, 3)}, ensure_ascii=False), flush=True)
        if result.returncode:
            raise OperationError(stage + '失败；未输出可能含凭据的底层错误，已有资源保留')
        return result.stdout

    def sql(statement, database='postgres'):
        if args.native:
            return run(['psql', '-X', '-w', '-d', database, '-v', 'ON_ERROR_STOP=1', '-At'],
                       '执行本地数据库显式初始化', statement)
        return run(base + ['exec', '-T', 'postgres', 'psql', '-U', 'liyuanci', '-d', database,
                          '-v', 'ON_ERROR_STOP=1', '-At'], '执行显式数据库初始化', statement)

    if not args.native:
        run(base + ['up', '-d', '--wait', 'postgres', 'redis'], '启动隔离数据库与缓存')
    exists = sql("SELECT EXISTS(SELECT 1 FROM pg_database WHERE datname='liyuanci_business');\n").strip() == 't'
    if not manifest.exists():
        # 旧数据库缺密钥时绝不生成替代文件，以免永久失去历史密文和身份检索能力。
        if exists:
            raise OperationError('已有业务数据库但缺原业务配置，请恢复原文件，禁止生成新密钥')
        cfg = {name: base64.b64encode(secrets.token_bytes(32)).decode() for name in
               ('WrapKey', 'LookupKey', 'SessionKey', 'CardKey', 'VerificationKey', 'WishKey', 'AdminKey')}
        cfg.update(AdminPassword=secrets.token_urlsafe(24), WeChatAppID='', WeChatSecret='',
                   ObjectDirectory='/var/lib/liyuanci/objects')
        roles = {'StudentDSN': 'identity_maintenance', 'ReviewDSN': 'admin_api', 'WishDSN': 'api',
                 'AdminDSN': 'admin_maintenance', 'WorkerDSN': 'domain_worker', 'MigrationDSN': 'migrator'}
        for field, role in roles.items():
            cfg[field] = 'host=postgres user=dev_' + role + ' password=' + secrets.token_hex(24) + ' dbname=liyuanci_business sslmode=disable'
        manifest.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
        if manifest.parent.stat().st_mode & 0o077:
            raise OperationError('业务凭据目录权限必须为0700')
        with manifest.open('x') as output:
            json.dump(cfg, output, indent=2)
        # 文件由非 root 容器读取，宿主父目录0700保护凭据；不挂载整个宿主目录。
        manifest.chmod(0o644)
    if manifest.parent.stat().st_mode & 0o077:
        raise OperationError('业务凭据目录权限必须为0700')
    cfg = json.loads(manifest.read_text())
    migration_file = manifest.with_name('business-migration.json')
    # 迁移身份只留在宿主独立文件，运行容器绝不挂载或继承它。
    migration_dsn = cfg.pop('MigrationDSN', None)
    if not migration_file.exists():
        if not migration_dsn:
            raise OperationError('缺少原迁移凭据，请恢复business-migration.json')
        with migration_file.open('x') as output:
            json.dump({'DSN': migration_dsn}, output)
        migration_file.chmod(0o600)
    if migration_dsn:
        saved = manifest.with_name('business.json.pending')
        with saved.open('w') as output:
            json.dump(cfg, output, indent=2)
        saved.chmod(0o644)
        saved.replace(manifest)
    # 已有配置只补独立工作身份，不替换历史密钥、口令或已有连接。
    if not cfg.get('WorkerDSN'):
        cfg['WorkerDSN'] = 'host=postgres user=dev_domain_worker password=' + secrets.token_hex(24) + ' dbname=liyuanci_business sslmode=disable'
        saved = manifest.with_name('business.json.pending')
        with saved.open('w') as output:
            json.dump(cfg, output, indent=2)
        saved.chmod(0o644)
        saved.replace(manifest)
    migration_dsn = json.loads(migration_file.read_text())['DSN']
    roles = {'StudentDSN': 'identity_maintenance', 'ReviewDSN': 'admin_api', 'WishDSN': 'api',
             'AdminDSN': 'admin_maintenance', 'WorkerDSN': 'domain_worker', 'MigrationDSN': 'migrator'}
    for field, role in roles.items():
        # 所有进入 SQL 的身份和口令必须匹配本脚本固定生成格式；外部修改不作为 SQL 执行。
        dsn = migration_dsn if field == 'MigrationDSN' else cfg[field]
        match = re.fullmatch(r'host=postgres user=dev_' + role + r' password=([0-9a-f]{48}) dbname=liyuanci_business sslmode=disable', dsn)
        if not match:
            raise OperationError('业务连接配置格式不匹配，拒绝初始化')
        group, login = 'liyuanci_' + role, 'dev_' + role
        sql("DO $$ BEGIN IF NOT EXISTS(SELECT 1 FROM pg_roles WHERE rolname='"+group+"') THEN CREATE ROLE "+group+" NOLOGIN; END IF; "
            "IF NOT EXISTS(SELECT 1 FROM pg_roles WHERE rolname='"+login+"') THEN CREATE ROLE "+login+" LOGIN PASSWORD '"+match[1]+"'; END IF; END $$;\n"
            "GRANT "+group+" TO "+login+";\n")
    if not exists:
        sql('CREATE DATABASE liyuanci_business OWNER dev_migrator;\n')
    # 迁移以专用迁移登录身份执行，权限矩阵由隔离数据库管理员显式应用。
    if args.host:
        from localdev import app_environment, host_dsn
        # 宿主不构建应用镜像；只有显式初始化使用迁移身份和数据库管理员。
        env['APP_DATABASE_DSN'] = host_dsn(migration_dsn, env)
        run(['go', 'run', './cmd/migrate', 'up'], '显式迁移宿主业务库')
        if args.native:
            # libpq环境供Go驱动连接本地管理员；业务运行身份仍来自独立文件。
            env['PGDATABASE'] = 'liyuanci_business'
            env['APP_DATABASE_DSN'] = 'sslmode=disable'
        else:
            env['APP_DATABASE_DSN'] = 'host=127.0.0.1 port=' + str(env.get('LIYUANCI_LOCAL_PG_PORT', '15432')) + ' user=liyuanci password=' + env['LIYUANCI_TEST_DB_PASSWORD'] + ' dbname=liyuanci_business sslmode=disable'
        run(['go', 'run', './cmd/migrate', 'permissions'], '显式配置宿主业务权限')
        if args.native:
            env = {key: value for key, value in env.items() if not key.startswith('PG')}
        env['LIYUANCI_DEV_BUSINESS_FILE'] = str(manifest)
        env = app_environment(entry, {key: value for key, value in env.items() if not key.startswith('APP_')})
        env['APP_ADMIN_BOOTSTRAP_LOGIN'] = args.admin_login
        run(['go', 'run', './cmd/dev-init'], '显式初始化宿主检索与后台账号')
    else:
        run(base + ['build', 'api'], '构建开发业务工具')
        env['APP_DATABASE_DSN'] = migration_dsn
        run(base + ['run', '--rm', '--no-deps', '-e', 'APP_DATABASE_DSN', '--entrypoint', './migrate', 'api', 'up'], '显式迁移业务库')
        env['APP_DATABASE_DSN'] = 'host=postgres user=liyuanci password=' + env['LIYUANCI_TEST_DB_PASSWORD'] + ' dbname=liyuanci_business sslmode=disable'
        run(base + ['run', '--rm', '--no-deps', '-e', 'APP_DATABASE_DSN', '--entrypoint', './migrate', 'api', 'permissions'], '显式配置业务权限')
        env['LIYUANCI_DEV_BUSINESS_FILE'] = str(manifest)
        env['APP_ENVIRONMENT'] = 'integration'
        env['APP_ADMIN_BOOTSTRAP_LOGIN'] = args.admin_login
        overlay = base + ['-f', str(ROOT / 'deploy/compose/business-dev.yaml')]
        run(overlay + ['run', '--rm', '--no-deps', '-e', 'APP_ENVIRONMENT', '-e', 'APP_ADMIN_BOOTSTRAP_LOGIN',
                       '--entrypoint', './dev-init', 'api'], '显式初始化检索与后台账号')
    # 所有步骤成功后才发布到日常启动配置；重复初始化保留现有密钥与账号。
    current = entry.read_text()
    if not any(line.startswith('LIYUANCI_DEV_BUSINESS_FILE=') for line in current.splitlines()):
        with entry.open('a') as output:
            output.write('\nLIYUANCI_DEV_BUSINESS_FILE=' + str(manifest) + '\n')
    print('开发业务初始化完成；扫描为开发模拟通过，微信与学校规则仍需真实供应商，生产未验证', flush=True)


if __name__ == '__main__':
    try:
        main()
    except (OperationError, OSError, ValueError, KeyError, subprocess.TimeoutExpired) as error:
        # 不回显文件正文、连接串或子进程输出。
        print(str(error) if isinstance(error, OperationError) else '开发业务初始化失败，请核对外部文件及Docker状态', flush=True)
        raise SystemExit(1)
