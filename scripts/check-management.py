#!/usr/bin/env python3
"""T003/T004/REQ-018、020：真实Docker管理工具及指标验收；不接触生产或既有项目。"""
import argparse
import base64
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
parser.add_argument('--metrics', action='store_true', help='配合middleware-only验证真实中间件指标及凭据失败')
parser.add_argument('--tool', choices=['pgadmin', 'redisinsight', 'rocketmq-dashboard'], help='只验证指定管理工具')
args = parser.parse_args()
if args.metrics and not args.middleware_only:
    parser.error('--metrics要求--middleware-only')
# 本机管理入口必须直连，避免继承宿主失效HTTP代理导致伪失败。
http = urllib.request.build_opener(urllib.request.ProxyHandler({}))

with tempfile.TemporaryDirectory(prefix='liyuanci-management-') as directory:
    root = Path(directory)
    env = os.environ.copy()
    env['COMPOSE_PROJECT_NAME'] = 'liyuanci-mgmt-' + secrets.token_hex(6)
    env['LIYUANCI_TEST_DB_PASSWORD'] = secrets.token_hex(24)
    env['LIYUANCI_PGADMIN_EMAIL'] = 'operator@example.com'
    redis_password = secrets.token_hex(24)
    pg_monitor_password = secrets.token_hex(24)
    redis_monitor_password = secrets.token_hex(24)
    files = {
        'LIYUANCI_PGADMIN_PASSWORD_FILE': secrets.token_hex(24),
        'LIYUANCI_POSTGRES_PASSWORD_FILE': secrets.token_hex(24),
        'LIYUANCI_REDIS_ACL_FILE': 'user default off\nuser operator on #' + hashlib.sha256(redis_password.encode()).hexdigest() + ' ~* +ping\n',
        'LIYUANCI_MQ_ACL_FILE': 'globalWhiteRemoteAddresses: []\naccounts:\n  - accessKey: testAdminKey\n    secretKey: ' + secrets.token_hex(24) + '\n    admin: true\n    defaultTopicPerm: DENY\n    defaultGroupPerm: DENY\n',
        'LIYUANCI_DASHBOARD_PROPERTIES_FILE': '',
    }
    if args.metrics:
        files['LIYUANCI_REDIS_ACL_FILE'] += 'user liyuanci_monitor on #' + hashlib.sha256(redis_monitor_password.encode()).hexdigest() + ' -@all +ping +info\n'
        files.update(LIYUANCI_PG_MONITOR_PASSWORD_FILE=pg_monitor_password,
                     LIYUANCI_REDIS_MONITOR_PASSWORDS_FILE=json.dumps({'redis://liyuanci_monitor@redis:6379': redis_monitor_password}),
                     LIYUANCI_GRAFANA_PASSWORD_FILE=secrets.token_hex(24),
                     LIYUANCI_GRAFANA_ENCRYPTION_KEY_FILE=secrets.token_hex(32))
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
            if args.metrics:
                # T004/REQ-018、020：显式创建只读统计身份，口令通过stdin传递，不用业务超级用户采集。
                subprocess.run(production + ['exec', '-T', 'postgres', 'sh', '-c',
                    'exec psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -v ON_ERROR_STOP=1'],
                    input="CREATE ROLE liyuanci_monitor LOGIN PASSWORD '" + pg_monitor_password + "'; GRANT pg_monitor TO liyuanci_monitor;\n",
                    text=True, capture_output=True, env=env, check=True, timeout=30)
                env.update(LIYUANCI_LOG_PROJECT=env['COMPOSE_PROJECT_NAME'], LIYUANCI_LOG_ENVIRONMENT='verification',
                           LIYUANCI_LOG_HOST='isolated', LIYUANCI_MONITORED_NETWORK=env['COMPOSE_PROJECT_NAME'] + '_foundation',
                           LIYUANCI_GRAFANA_PORT='13997', LIYUANCI_GRAFANA_ROOT_URL='http://127.0.0.1:13997/',
                           LIYUANCI_LOG_RETENTION='72h')
                observer = common + ['-p', env['COMPOSE_PROJECT_NAME'] + '-observability',
                    '-f', 'compose.observability.yaml', '-f', 'compose.observability.production.yaml',
                    '-f', 'compose.metrics.yaml', '-f', 'compose.metrics.middleware.yaml']
                auth = base64.b64encode(('operator:' + files['LIYUANCI_GRAFANA_PASSWORD_FILE']).encode()).decode()

                def api(path):
                    request = urllib.request.Request('http://127.0.0.1:13997' + path, headers={'Authorization': 'Basic ' + auth})
                    with http.open(request, timeout=10) as response:
                        return json.load(response)

                def query(expression):
                    import urllib.parse
                    return api('/api/datasources/proxy/uid/liyuanci-prometheus/api/v1/query?' +
                               urllib.parse.urlencode({'query': expression}))['data']['result']

                def wait_value(expression, value, description):
                    deadline = time.monotonic() + 150
                    while time.monotonic() < deadline:
                        try:
                            result = query(expression)
                            if result and all(row['value'][1] == str(value) for row in result):
                                print('PASS ' + description, flush=True)
                                return
                        except (OSError, ValueError, KeyError):
                            pass
                        time.sleep(2)
                    raise RuntimeError('timeout: ' + description)

                try:
                    run(observer + ['up', '-d'], env)
                    wait_value('count(up == 1)', 7, '七个真实指标目标up=1')
                    for expression, value in [('pg_up', 1), ('redis_up', 1),
                        ('pg_exporter_last_scrape_error', 0), ('redis_exporter_last_scrape_error', 0)]:
                        wait_value(expression, value, expression)
                    wait_value('count(count by(__name__)({job="rocketmq-broker",__name__=~"rocketmq_.*"})) > bool 0',
                               1, 'RocketMQ原生指标存在')
                    assert api('/api/dashboards/uid/liyuanci-middleware')['dashboard']['uid'] == 'liyuanci-middleware'
                    deadline = time.monotonic() + 90
                    expected = {'T004 PostgreSQL monitor connection failed', 'T004 PostgreSQL scrape error',
                                'T004 Redis monitor connection failed', 'T004 Redis scrape error'}
                    while True:
                        groups = api('/api/prometheus/grafana/api/v1/rules')['data']['groups']
                        rules = {rule['name']: rule for group in groups for rule in group['rules']}
                        if expected <= rules.keys() and all(rules[name].get('health') == 'ok' for name in expected):
                            print('PASS 中间件看板、四条规则实际求值及生产监控加密密钥注入', flush=True)
                            break
                        if time.monotonic() >= deadline:
                            raise RuntimeError('中间件告警规则求值未通过')
                        time.sleep(2)
                    # 网络up不能替代认证；错误监控凭据必须反映到pg_up/redis_up，并可恢复。
                    for service, variable, original, metric in [
                        ('postgres-exporter', 'LIYUANCI_PG_MONITOR_PASSWORD_FILE', pg_monitor_password, 'pg_up'),
                        ('redis-exporter', 'LIYUANCI_REDIS_MONITOR_PASSWORDS_FILE',
                         files['LIYUANCI_REDIS_MONITOR_PASSWORDS_FILE'], 'redis_up')]:
                        wrong = secrets.token_hex(24)
                        if service == 'redis-exporter':
                            wrong = json.dumps({'redis://liyuanci_monitor@redis:6379': wrong})
                        Path(env[variable]).write_text(wrong)
                        run(observer + ['restart', service], env)
                        wait_value(metric, 0, service + '错误凭据被识别')
                        Path(env[variable]).write_text(original)
                        run(observer + ['restart', service], env)
                        wait_value(metric, 1, service + '正确凭据恢复')
                    denied = subprocess.run(production + ['exec', '-T', 'redis', 'redis-cli', '--raw'],
                        input='AUTH liyuanci_monitor ' + redis_monitor_password + '\r\nGET private-test-key\r\n',
                        text=True, capture_output=True, env=env, check=True, timeout=30)
                    assert 'NOPERM' in denied.stdout
                    print('PASS Redis监控身份不能读取业务键值；project=' + env['COMPOSE_PROJECT_NAME'], flush=True)
                finally:
                    run(observer + ['down'], env)
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
