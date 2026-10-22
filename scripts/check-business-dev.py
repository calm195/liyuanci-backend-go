#!/usr/bin/env python3
"""T008/T011/T014、REQ-002/003/015/018：独立Docker项目验证真实业务启动、后台登录及失败关闭。"""
import hashlib
import json
import os
from pathlib import Path
import secrets
import socket
import subprocess
import tempfile
import urllib.error
import urllib.request

ROOT = Path(__file__).resolve().parents[1]


def main():
    project = 'liyuanci-business-check-' + secrets.token_hex(5)
    # 停止始终保留数据卷，因此凭据目录也保留，不能制造有旧卷却失钥的环境。
    directory = Path(tempfile.mkdtemp(prefix=project)).resolve()
    config = directory / 'config'
    entry = config / 'operations.env'
    with socket.socket() as listener:
        listener.bind(('127.0.0.1', 0))
        port = listener.getsockname()[1]

    def run(command):
        result = subprocess.run(command, cwd=ROOT, text=True, capture_output=True, timeout=1200)
        print(result.stdout, end='', flush=True)
        if result.returncode:
            raise RuntimeError('隔离业务检查失败；项目和凭据保留：' + project + ' ' + str(directory))

    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))

    def http(method, path, body=None, token=None):
        headers = {'Content-Type': 'application/json'}
        if token:
            headers['Authorization'] = 'Bearer ' + token
        request = urllib.request.Request('http://127.0.0.1:' + str(port) + path,
                                         data=None if body is None else json.dumps(body).encode(),
                                         headers=headers, method=method)
        try:
            response = opener.open(request, timeout=10)
        except urllib.error.HTTPError as error:
            response = error
        with response:
            data = response.read()
            assert response.headers.get('X-Request-ID'), '缺少请求关联'
            return response.status, json.loads(data) if data else None

    stop = [str(ROOT / 'scripts/stop.sh'), 'dev', '--project', project, '--remove']
    try:
        run([str(ROOT / 'scripts/init-dev.sh'), '--project', project, '--config-dir', str(config)])
        with entry.open('a') as output:
            output.write('LIYUANCI_DEV_API_PORT=' + str(port) + '\n')
        initialize = ['python3', 'scripts/init-business-dev.py', '--project', project,
                      '--env-file', str(entry), '--admin-login', 'explicit-check-admin']
        run(initialize)
        manifest = config / 'business.json'
        before = hashlib.sha256(manifest.read_bytes()).digest()
        run(initialize)
        assert before == hashlib.sha256(manifest.read_bytes()).digest(), '重复初始化改写凭据'
        run([str(ROOT / 'scripts/start.sh'), 'dev', '--project', project, '--env-file', str(entry),
             '--without-management', '--without-monitoring', '--bootstrap-probe'])
        for method, path, expected in [('GET', '/healthz', 200), ('GET', '/readyz', 503),
            ('GET', '/api/v1', 404), ('GET', '/api/v1/me', 401),
            ('POST', '/api/v1/auth/wechat-sessions', 400),
            ('POST', '/api/v1/me/student-card-assets', 401),
            ('GET', '/api/v1/me/verification-request', 401),
            ('GET', '/api/v1/admin/identity-reviews', 401),
            ('GET', '/api/v1/admin/accounts', 401), ('POST', '/api/v1/wishes', 401),
            ('GET', '/api/v1/me/wishes', 401), ('GET', '/api/v1/wishes', 200)]:
            status, _ = http(method, path)
            assert status == expected, (method, path, status, expected)
        cfg = json.loads(manifest.read_text())
        status, session = http('POST', '/api/v1/admin/sessions', {'login_name': 'explicit-check-admin', 'password': cfg['AdminPassword']})
        assert status == 200, ('后台真实登录失败', status)
        status, _ = http('GET', '/api/v1/admin/accounts', token=session['data']['token'])
        assert status == 200, ('后台授权查询失败', status)
        status, _ = http('POST', '/api/v1/auth/wechat-sessions', {'code': 'no-synthetic-login', 'client_request_id': 'check'})
        assert status == 503, ('缺少微信配置没有失败关闭', status)
        print('PASS 业务路由、真实PG/Redis后台登录/授权、公开列表、重复初始化及微信缺配置失败关闭', flush=True)
        run([str(ROOT / 'scripts/stop.sh'), 'dev', '--project', project])
        run([str(ROOT / 'scripts/start.sh'), 'dev', '--project', project, '--env-file', str(entry),
             '--without-management', '--without-monitoring', '--no-build'])
        status, _ = http('GET', '/api/v1/admin/accounts', token=session['data']['token'])
        assert status == 200, ('重启丢失已有会话', status)
        print('PASS 普通重启不执行迁移/重置账号，原会话仍可授权；生产和外部供应商未验证', flush=True)
    finally:
        run(stop)
        print('隔离项目已停止；数据卷与原凭据保留：' + project + ' ' + str(directory), flush=True)


if __name__ == '__main__':
    main()
