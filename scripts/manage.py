#!/usr/bin/env python3
"""简化原生入口：所有进程使用同一份公共配置和环境覆盖；不隐式启停数据库。"""
import argparse
import base64
import os
from pathlib import Path
import secrets
import sys
import yaml_settings as settings

ROOT = Path(__file__).resolve().parents[1]


def initialize(path, environment):
    """只新建不存在的私有环境文件；不替换已有密钥或账号。"""
    path = Path(path).resolve()
    if path.exists():
        raise ValueError('私有配置已存在，拒绝覆盖')
    source = ROOT / 'configs' / (environment + '.example.yaml')
    values = settings.read(source)
    if environment == 'development':
        values['masterkey'] = base64.b64encode(secrets.token_bytes(32)).decode()
        values['development']['adminpassword'] = secrets.token_urlsafe(24)
    if not (path.parent / 'common.yaml').is_file():
        raise ValueError('环境覆盖旁必须存在common.yaml')
    # 独占创建，防止并发初始化覆盖历史文件。
    with path.open('x', encoding='utf-8') as output:
        os.chmod(path, 0o600)
        output.write(settings.dumps(values))
    print('私有配置已创建；请填写database下的主机、库名、账号口令、Redis及所需供应商配置后运行check。')


def command(action, config, kind):
    """只把进程动作作为命令行参数，数据库及业务配置全部留在YAML。"""
    values = settings.layers(config)
    development = values.get('mode') == 'development'
    if action in ('run', 'check', 'doctor'):
        args = ['server', '-config', str(config)]
        if action != 'run':
            args += ['-check-config' if action == 'check' else '-check']
    elif action == 'init':
        args = ['dev-init' if development else 'business-init', '-config', str(config)]
    elif action == 'worker':
        if development:
            names = {'card': 'card-worker', 'content': 'content-worker', 'whispers': 'content-worker',
                    'account': 'privacy-worker', 'evidence': 'privacy-worker'}
            if kind not in names:
                raise ValueError('工作职责无效')
            args = [names[kind], '-config', str(config), '-kind', kind]
        else:
            args = ['business-worker', '-config', str(config), '-kind', kind]
    elif action in ('migrate', 'permissions', 'opaque-blocks'):
        args = ['migrate', '-config', str(config), 'up' if action == 'migrate' else action]
    else:
        raise ValueError('操作无效')
    binary = ROOT / 'bin' / args[0]
    if not binary.is_file():
        raise ValueError('请先执行make build')
    return [str(binary), *args[1:]]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        'action',
        choices=['configure', 'run', 'check', 'doctor', 'init', 'worker', 'migrate', 'permissions', 'opaque-blocks']
    )
    parser.add_argument('--config', default='configs/development.yaml')
    parser.add_argument('--environment', choices=['development', 'production'], default='development')
    parser.add_argument('--kind', choices=['card', 'content', 'whispers', 'account', 'evidence'], default='content')
    args = parser.parse_args()
    path = Path(args.config).resolve()
    if args.action == 'configure':
        initialize(path, args.environment)
        return
    # 目录仅创建结构，不清空状态、不生成新密钥；普通启动不执行DDL或初始化账号。
    values = settings.layers(path)
    local = values.get('development', {}) if values.get('mode') == 'development' else values.get('business', {}).get('runtime', {})
    for key in (('journalfile', 'objectdirectory') if args.action in ('run', 'worker', 'init') else ()):
        value = local.get(key)
        if value:
            directory = Path(value)
            if not directory.is_absolute():
                directory = path.parent / directory
            if key == 'journalfile':
                directory = directory.parent
            directory.mkdir(mode=0o700, parents=True, exist_ok=True)
    argv = command(args.action, path, args.kind)
    os.execve(argv[0], argv, settings.process_environment())


if __name__ == '__main__':
    try:
        main()
    except (OSError, ValueError, KeyError):
        print('操作未完成，请检查环境YAML、目录权限和构建产物；凭据不会输出。', file=sys.stderr)
        sys.exit(1)
