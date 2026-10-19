#!/usr/bin/env python3
"""T003/T004/REQ-018、020：真实Docker管理工具冒烟；不接触生产或既有项目。"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import secrets
import subprocess
import tempfile
import time
import urllib.request


def run(args, env, capture=False):
    # 命令参数只有secret路径，禁止打印配置展开内容和随机口令。
    return subprocess.run(args, env=env, check=True, text=True,
                          stdout=subprocess.PIPE if capture else None, timeout=900).stdout


parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('--config-only', action='store_true')
parser.add_argument('--middleware-only', action='store_true', help='隔离验证生产形态中间件，不启动管理工具')
parser.add_argument('--tool', choices=['pgadmin', 'redisinsight', 'rocketmq-dashboard'], help='只验证指定管理工具')
args = parser.parse_args()
# 本机管理入口必须直连，避免继承宿主失效HTTP代理导致伪失败。
http = urllib.request.build_opener(urllib.request.ProxyHandler({}))

with tempfile.TemporaryDirectory(prefix='liyuanci-management-') as directory:
    root = Path(directory)
    env = os.environ.copy()
    env['COMPOSE_PROJECT_NAME'] = 'liyuanci-mgmt-' + secrets.token_hex(6)
    env['LIYUANCI_TEST_DB_PASSWORD'] = secrets.token_hex(24)
    env['LIYUANCI_PGADMIN_EMAIL'] = 'operator@example.com'
    redis_password = secrets.token_hex(24)
    files = {
        'LIYUANCI_PGADMIN_PASSWORD_FILE': secrets.token_hex(24),
        'LIYUANCI_POSTGRES_PASSWORD_FILE': secrets.token_hex(24),
        'LIYUANCI_REDIS_ACL_FILE': 'user default off\nuser operator on #' + hashlib.sha256(redis_password.encode()).hexdigest() + ' ~* +ping\n',
        'LIYUANCI_MQ_ACL_FILE': 'globalWhiteRemoteAddresses: []\naccounts:\n  - accessKey: testAdminKey\n    secretKey: ' + secrets.token_hex(24) + '\n    admin: true\n    defaultTopicPerm: DENY\n    defaultGroupPerm: DENY\n',
        'LIYUANCI_DASHBOARD_PROPERTIES_FILE': '',
    }
    for variable, value in files.items():
        path = root / variable
        path.write_text(value)
        path.chmod(0o644)  # 临时目录0700；容器非root进程须能读取挂载文件。
        env[variable] = str(path)
    common = ['docker', 'compose', '--progress', 'plain']
    integration = common + ['-f', 'compose.integration.yaml', '-f', 'compose.management.yaml', '--profile', 'management']
    production = common + ['-f', 'compose.production.yaml', '-f', 'compose.management.yaml', '--profile', 'management']
    for command in (integration, production):
        config = json.loads(run(command + ['config', '--format', 'json'], env, True))
        assert config['networks']['foundation']['internal']
        assert any(secret['source'] == 'dashboard_properties' and secret['target'] == 'dashboard.properties'
                   for secret in config['services']['rocketmq-dashboard']['secrets'])
        for name, service in config['services'].items():
            if name in ('postgres', 'redis', 'namesrv', 'broker'):
                assert not service.get('ports'), name
            if name in ('pgadmin', 'redisinsight', 'rocketmq-dashboard'):
                assert '@sha256:' in service['image'], name
                assert all(port['host_ip'] == '127.0.0.1' for port in service['ports']), name
        if command == production:
            assert not {'api', 'worker', 'probe', 'migrate'} & config['services'].keys()
    print('T003/T004/REQ-018,020 Compose isolation/config checks passed', flush=True)
    if args.config_only:
        raise SystemExit(0)
    if args.middleware_only:
        try:
            run(production + ['up', '-d', '--wait', '--wait-timeout', '240',
                              'postgres', 'redis', 'namesrv', 'broker'], env)
            denied = run(production + ['exec', '-T', 'redis', 'redis-cli', 'ping'], env, True)
            assert 'NOAUTH' in denied
            # 口令只经stdin发送，不放入进程参数或日志；专用测试账号只允许PING。
            result = subprocess.run(production + ['exec', '-T', 'redis', 'redis-cli', '--raw'],
                                    input='AUTH operator ' + redis_password + '\r\nPING\r\n',
                                    text=True, capture_output=True, env=env, check=True, timeout=30)
            assert result.stdout.split() == ['OK', 'PONG']
            denied_mq = subprocess.run(production + ['exec', '-T', 'broker', 'sh', 'mqadmin',
                                      'updateTopic', '-n', 'namesrv:9876', '-c',
                                      'LiyuanciProduction', '-t', 'unauthorized_test'],
                                      text=True, capture_output=True, env=env, timeout=30)
            assert any(reason in denied_mq.stdout + denied_mq.stderr for reason in ('No accessKey', 'AclException', 'CODE: 16')), 'MQ must explicitly reject anonymous authorization'
            print('T004 isolated production-shape middleware passed: PG/MQ healthy, Redis auth allow/deny, MQ anonymous write denied', flush=True)
        finally:
            run(production + ['down'], env)
        raise SystemExit(0)
    tools = [args.tool] if args.tool else ['pgadmin', 'redisinsight', 'rocketmq-dashboard']
    try:
        run(integration + ['up', '-d', '--wait', '--wait-timeout', '240',
                           'postgres', 'redis', 'namesrv', 'broker',
                           *tools], env)
        endpoints = {'pgadmin': 'http://127.0.0.1:15050/misc/ping',
                     'redisinsight': 'http://127.0.0.1:15540/api/health/',
                     'rocketmq-dashboard': 'http://127.0.0.1:18081/'}
        for tool in tools:
            url = endpoints[tool]
            deadline = time.monotonic() + 180
            while True:
                try:
                    with http.open(url, timeout=5) as response:
                        assert response.status == 200
                    print('T004 management HTTP passed: ' + url, flush=True)
                    break
                except Exception:
                    if time.monotonic() >= deadline:
                        raise
                    time.sleep(2)
        if 'rocketmq-dashboard' in tools:
            with http.open('http://127.0.0.1:18081/cluster/list.query', timeout=15) as response:
                result = json.load(response)
                assert result.get('status') == 0 and 'broker-a' in json.dumps(result.get('data'))
            print('T004 dashboard real broker discovery passed; production ACL not tested', flush=True)
    finally:
        # 仅停止本次唯一项目；遵守既有保留卷策略，不删除任何数据卷。
        run(integration + ['down'], env)
