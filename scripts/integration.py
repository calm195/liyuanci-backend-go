#!/usr/bin/env python3
"""在一次性原生PostgreSQL/Redis实例中验证应用和领域；结束后删除本轮数据。"""
import argparse
import json
from pathlib import Path
import secrets
import shutil
import socket
import subprocess
import sys
import tempfile
import time
import yaml_settings as yamlio

ROOT = Path(__file__).resolve().parents[1]
# 固定包与必需用例，运行后逐项核验实际结果；不接受外部数据库地址。
SUITES = {
    'adminauth': ('./internal/modules/adminauth', ('TestPostgresAdmin', 'TestPostgresDashboard')),
    'runtime': (
        './internal/app',
        ('TestPostgresLocalBusiness', 'TestPostgresWhisperRuntime', 'TestPostgresBusinessDatabaseRuntime',
         'TestPostgresDevelopmentCommands', 'TestPostgresDevelopmentRecoveryGate')
    ),
    'durable': ('./internal/execution/durable', ('TestPostgresDurable', 'TestPostgresConsumer', 'TestPostgresRecovery')),
    'dictionary': ('./internal/modules/dictionary', ('TestPostgresDictionaries',)),
    'moduleguide': ('./internal/modules/moduleguide', ('TestPostgresGuide',)),
    'wish_lifecycle': ('./internal/modules/wish', ('TestPostgresLifecycle',)),
    'studentcard': ('./internal/modules/studentcard', ('TestPostgresStudentCards',)),
    'verification': ('./internal/modules/verification', ('TestPostgresVerification',)),
    'resourceplaza': ('./internal/modules/resourceplaza', ('TestPostgresHTTP',)),
    'moderation': (
        './internal/modules/moderation',
        ('TestModerationCore', 'TestCrisisCare', 'TestPrivacyDeletion', 'TestApplicationMatching',
         'TestChatDelivery', 'TestStorySharing', 'TestSafetyGovernance', 'TestAdminUsersQuery')
    ),
    'whisper_review': ('./internal/modules/whisper', ('TestPostgresWhisperModeration',)),
    'studentauth': ('./internal/modules/studentauth', ('TestPostgresStudent',)),
    'whisper_text': ('./internal/modules/whisper', ('TestPostgresWhisperText',)),
    'redis_rate': ('./internal/adapters/redis/whisperrate', ('TestRedisWhisperAdmission',)),
    'command': (
        './internal/execution/command',
        ('TestPostgresCommands', 'TestBackgroundPlan', 'TestPostgresWhisperLocks')
    ),
    'wish_draft': ('./internal/modules/wish', ('TestPostgresCreateDraft',)),
    'wish_http': ('./internal/httpapi', ('TestPostgresWishHTTP',)),
    'identity': ('./internal/modules/identity', ('TestPostgresIdentity',)),
    'migration': (
        './internal/migration',
        (
            'TestPostgresMigrations',
            'TestPostgresApplicationMigrations',
            'TestBusinessSchema',
            'TestRecoverySchema',
            'TestPostgresStoryPublication',
            'TestPostgresWhisperFoundation'
        )
    ),
}
# 原地上下行/升级测试需要各自空库；只在本轮临时集群创建这些固定名称的数据库。
MIGRATION_DATABASES = {
    'adminauth_test_dsn': 'adminauth_test',
    'studentauth_test_dsn': 'studentauth_test',
    'migration_test_dsn': 'migration_test',
    'schema_test_dsn': 'schema_test',
    'recovery_test_dsn': 'recovery_test',
    'story_publication_test_dsn': 'story_publication_test',
    'command_test_dsn': 'command_test',
    'identity_test_dsn': 'identity_test',
    'wish_test_dsn': 'wish_test',
    'wish_http_test_dsn': 'wish_http_test',
}


def executable(name):
    found = shutil.which(name)
    if found:
        return found
    for folder in [
        Path('/opt/homebrew/opt/postgresql@18/bin'),
        *sorted(Path('/usr/lib/postgresql').glob('*/bin'), reverse=True)
    ]:
        candidate = folder / name
        if candidate.is_file():
            return str(candidate)
    raise ValueError('缺少原生测试依赖：' + name)


def port():
    with socket.socket() as sock:
        sock.bind(('127.0.0.1', 0))
        return sock.getsockname()[1]


def start_redis(executable_path, number, password, env):
    """只启动本轮临时实例；通过标准输入提供原生配置，不读取旧应用环境变量。"""
    process = subprocess.Popen(
        [executable_path, '-'],
        stdin=subprocess.PIPE,
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
        env=env,
    )
    configuration = (
        'bind 127.0.0.1\nport ' + str(number) + '\nsave ""\nappendonly no\n'
        + ('requirepass ' + password + '\n' if password else '')
    )
    process.stdin.write(configuration.encode())
    process.stdin.close()
    return process


def wait_for_redis(numbers):
    """两个实例共享同一启动预算；超时交给外层统一回收本轮子进程。"""
    deadline = time.monotonic() + 10
    for number in numbers:
        while True:
            try:
                with socket.create_connection(('127.0.0.1', number), timeout=0.2):
                    break
            except OSError:
                if time.monotonic() > deadline:
                    raise ValueError('测试Redis启动失败')
                time.sleep(0.05)


def run_suite(name, config, env):
    """退出码与Go结构化事件同时通过才算成功；未匹配或跳过用例不能冒充集成验收。"""
    package, tests = SUITES[name]
    pattern = '^(' + '|'.join(tests) + ')$'
    result = subprocess.run(
        ['go', 'test', package, '-count=1', '-timeout=8m', '-json', '-run', pattern,
        '-args', '-integration-config', str(config)],
        cwd=ROOT, env=env, stdout=subprocess.PIPE, text=True,
    )
    expected_package = 'github.com/calm195/liyuanci-backend-go/' + package[2:]
    started, passed, skipped = set(), set(), set()
    invalid, failed, package_passed = False, False, False
    for line in result.stdout.splitlines():
        try:
            event = json.loads(line)
        except json.JSONDecodeError:
            invalid = True
            continue
        if not isinstance(event, dict):
            invalid = True
            continue
        output = event.get('Output')
        if isinstance(output, str):
            # 仍显示原始测试说明，机器事件仅用于核验，不写入仓库或持久日志。
            print(output, end='', flush=True)
        if event.get('Package') != expected_package:
            continue
        action, test = event.get('Action'), event.get('Test', '')
        if not isinstance(test, str):
            invalid = True
            continue
        if action == 'fail':
            failed = True
        if not test:
            package_passed = action == 'pass' or package_passed
            continue
        root = test.split('/', 1)[0]
        if root not in tests:
            continue
        if action == 'skip':
            skipped.add(test)
        if test == root:
            if action == 'run':
                started.add(test)
            elif action == 'pass':
                passed.add(test)
    if result.returncode or failed:
        raise ValueError('原生集成失败：' + name)
    if invalid:
        raise ValueError('原生集成结果事件无效：' + name)
    if skipped:
        raise ValueError('原生集成用例被跳过：' + ', '.join(sorted(skipped)))
    missing = set(tests) - (started & passed)
    if missing:
        raise ValueError('原生集成缺少执行通过的必需用例：' + ', '.join(sorted(missing)))
    if not package_passed:
        raise ValueError('原生集成缺少测试包完成结果：' + name)
    print('原生专项通过：' + name + '，必需用例 ' + str(len(tests)) + ' 项。', flush=True)


def run(suite='all'):
    postgres = {name: executable(name) for name in ('initdb', 'pg_ctl', 'psql')}
    redis = executable('redis-server')
    env = yamlio.process_environment()
    started, children = False, []
    with tempfile.TemporaryDirectory(prefix='liyuanci-integration-') as temp:
        root = Path(temp)
        pgdata = root / 'postgres'
        pgport, redisport, authport = port(), port(), port()
        while len({pgport, redisport, authport}) != 3:
            pgport, redisport, authport = port(), port(), port()
        dbpassword, redispassword = secrets.token_urlsafe(24), secrets.token_urlsafe(24)
        # initdb只接收本轮临时权限0600输入；原始秘密保存在测试YAML，不是配置入口。
        pw = root / 'init-password'
        pw.write_text(dbpassword)
        pw.chmod(0o600)
        try:
            subprocess.run(
                [
                    postgres['initdb'],
                    '-D',
                    str(pgdata),
                    '-U',
                    'test_admin',
                    '--auth-local=trust',
                    '--auth-host=scram-sha-256',
                    '--pwfile',
                    str(pw)
                ],
                env=env,
                check=True,
                stdout=subprocess.DEVNULL,
                stderr=subprocess.DEVNULL
            )
            pw.unlink()
            subprocess.run(
                [
                    postgres['pg_ctl'],
                    '-D',
                    str(pgdata),
                    '-l',
                    str(root / 'postgres.log'),
                    '-o',
                    '-h 127.0.0.1 -p ' + str(pgport) + ' -k ' + str(root),
                    '-w',
                    'start'
                ],
                env=env,
                check=True,
                stdout=subprocess.DEVNULL
            )
            started = True
            subprocess.run(
                [
                    postgres['psql'],
                    '-h',
                    str(root),
                    '-p',
                    str(pgport),
                    '-U',
                    'test_admin',
                    '-d',
                    'postgres',
                    '-v',
                    'ON_ERROR_STOP=1',
                    '-c',
                    'CREATE DATABASE liyuanci'
                ],
                env=env,
                check=True,
                stdout=subprocess.DEVNULL
            )
            for number, password in [(redisport, ''), (authport, redispassword)]:
                children.append(start_redis(redis, number, password, env))
            wait_for_redis((redisport, authport))
            dsn = f'host=127.0.0.1 port={pgport} user=test_admin password={dbpassword} dbname=liyuanci sslmode=disable '
            # 测试配置只引用本轮实例与已构建二进制，不接入已有业务或生产数据库。
            values = {
                'whisper_text_test_dsn': dsn,
                'whisper_command_test_dsn': dsn,
                'studentauth_test_redis': f'127.0.0.1:{redisport}',
                'adminauth_test_redis': f'127.0.0.1:{redisport}',
                'whisper_redis_test_address': f'127.0.0.1:{redisport}',
                'business_runtime_redis_address': f'127.0.0.1:{authport}',
                'business_runtime_redis_password': redispassword,
                'whisper_server_binary': str(ROOT / 'bin/server'),
                'business_runtime_worker_binary': str(ROOT / 'bin/worker'),
                'business_runtime_init_binary': str(ROOT / 'bin/init'),
                'whisper_content_binary': str(ROOT / 'bin/worker'),
                'whisper_privacy_binary': str(ROOT / 'bin/worker'),
            }
            values.update(resource_test_dsn=dsn, moderation_test_dsn=dsn, durable_test_dsn=dsn, wish_lifecycle_test_dsn=dsn,
                          verification_test_dsn=dsn, studentcard_test_dsn=dsn, studentcard_test_redis=f'127.0.0.1:{redisport}')
            if suite in (
                'all', 'migration', 'studentauth', 'adminauth',
                'command', 'wish_draft', 'wish_http', 'identity'
            ):
                for key, database in MIGRATION_DATABASES.items():
                    subprocess.run(
                        [
                            postgres['psql'],
                            '-h',
                            str(root),
                            '-p',
                            str(pgport),
                            '-U',
                            'test_admin',
                            '-d',
                            'postgres',
                            '-v',
                            'ON_ERROR_STOP=1',
                            '-c',
                            'CREATE DATABASE ' + database
                        ],
                        env=env,
                        check=True,
                        stdout=subprocess.DEVNULL
                    )
                    values[key] = dsn.replace('dbname=liyuanci ', 'dbname=' + database + ' ', 1)
                # 树洞升级场景自行创建随机子库，只借用本轮临时父库连接。
                values['whisper_schema_test_dsn'] = dsn
            config = root / 'tests.yaml'
            yamlio.write(config, values)
            # 同一临时集群内顺序执行，避免历史权限夹具并发修改集群级角色。
            for name in SUITES if suite == 'all' else (suite,):
                print('运行原生集成：' + name, flush=True)
                run_suite(name, config, env)
        finally:
            for proc in children:
                proc.terminate()
                proc.wait(timeout=10)
            if started:
                subprocess.run(
                    [postgres['pg_ctl'], '-D', str(pgdata), '-m', 'fast', '-w', 'stop'],
                    env=env,
                    check=True,
                    stdout=subprocess.DEVNULL
                )
    print('原生集成完成，本轮PG/Redis及临时数据已清理。')


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--suite', choices=('all', *SUITES), default='all', help='默认执行全部专项；实例和数据均为本轮临时创建')
    args = parser.parse_args()
    try:
        run(args.suite)
    except (OSError, ValueError, subprocess.SubprocessError):
        print('原生集成未完成，请核对依赖和测试输出；未操作已有数据库。', file=sys.stderr)
        sys.exit(1)
