#!/usr/bin/env python3
"""显式为本地业务合并常见三类词库，通过后台认证及版本化发布保留审计和旧快照。"""
import argparse
import json
from pathlib import Path
import sys
from urllib import error, parse, request
import uuid


def merge_entries(current, seeds):
    """完整保留现有词条，只追加尚不存在的种子；重复执行不再发布。"""
    return sorted(set(current).union(seeds))


def initialize(call, seeds):
    """调用既有正式操作而非直接写表；代次冲突立即失败，不能覆盖并发发布。"""
    results = []
    for kind in ('crisis', 'school', 'ordinary_violation'):
        page = call('GET', '/wordlists?limit=100')
        head = next(x for x in page['heads'] if x['kind'] == kind)
        current = []
        if head['effective_id'] is not None:
            current = call('GET', '/wordlists/' + head['effective_id'])['entries']
        merged = merge_entries(current, seeds[kind])
        if merged == sorted(current):
            results.append({'kind': kind, 'changed': False, 'entry_count': len(merged)})
            continue
        draft = call('POST', '/wordlists/versions', {
            'kind': kind, 'entries': merged, 'expected_version': head['version'],
        })
        # 创建会推进head一次；沿用所读基准，其他写者插入会被发布入口409拒绝。
        call('POST', '/wordlists/' + draft['id'] + '/publish', {
            'expected_version': draft['version'], 'expected_head_version': head['version'] + 1,
        })
        results.append({'kind': kind, 'changed': True, 'entry_count': len(merged),
                        'added_count': len(merged) - len(current)})
    return results


class NoRedirect(request.HTTPRedirectHandler):
    """后台口令和会话不能随重定向发送到其他端点。"""
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def main():
    """只连接显式本地API；不打印口令、令牌、词条或底层网络错误。"""
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--api-url', default='http://127.0.0.1:18080')
    parser.add_argument('--business-file', default='.local/local/business.host.json')
    parser.add_argument('--seeds', default='configs/examples/moderation-wordlists.json')
    parser.add_argument('--login', default='developer')
    args = parser.parse_args()
    endpoint = parse.urlsplit(args.api_url)
    if endpoint.scheme != 'http' or endpoint.hostname not in ('localhost', '127.0.0.1', '::1') or endpoint.username or endpoint.password or endpoint.path or endpoint.query or endpoint.fragment:
        raise ValueError('初始化仅允许本地HTTP入口')
    cfg = json.loads(Path(args.business_file).read_text())
    seeds = json.loads(Path(args.seeds).read_text())
    if set(seeds) != {'crisis', 'school', 'ordinary_violation'} or any(not isinstance(items, list) or not items or any(not isinstance(x, str) or not x.strip() for x in items) for items in seeds.values()):
        raise ValueError('三类种子词库格式无效')
    opener = request.build_opener(request.ProxyHandler({}), NoRedirect())
    token = None

    def call(method, path, data=None):
        headers = {'Content-Type': 'application/json'}
        if token:
            headers['Authorization'] = 'Bearer ' + token
        if method != 'GET':
            headers['Idempotency-Key'] = str(uuid.uuid4())
        req = request.Request(args.api_url + '/api/v1/admin' + path,
                              data=json.dumps(data).encode() if data is not None else None,
                              headers=headers, method=method)
        with opener.open(req, timeout=15) as response:
            if response.status == 204:
                return None
            body = json.load(response)
            if body.get('code') != 0:
                raise ValueError('后台操作未完成')
            return body['data']

    session = call('POST', '/sessions', {'login_name': args.login, 'password': cfg['AdminPassword']})
    token = session['token']
    try:
        result = initialize(call, seeds)
    finally:
        call('DELETE', '/session')
    print(json.dumps({'result': 'success', 'wordlists': result}, ensure_ascii=False))


if __name__ == '__main__':
    try:
        main()
    except (OSError, ValueError, KeyError, StopIteration, error.URLError):
        print('词库初始化未完成，请检查本地配置、后台授权或版本冲突；已发布的类别保留，修复后可重复执行。', file=sys.stderr)
        sys.exit(1)
