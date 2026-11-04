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
        values['business']['adminpassword'] = secrets.token_urlsafe(24)
    if not (path.parent / 'common.yaml').is_file():
        raise ValueError('环境覆盖旁必须存在common.yaml')
    # 独占创建，防止并发初始化覆盖历史文件。
    with path.open('x', encoding='utf-8') as output:
        os.chmod(path, 0o600)
        output.write(settings.dumps(values))
    print('私有配置已创建；请填写database下的主机、库名、账号口令、Redis及所需供应商配置后运行check。')


def command(action, config, kind, check_config=False, check=False, recover=False):
    """只把进程动作作为命令行参数，数据库及业务配置全部留在YAML。"""
    settings.layers(config)
    if sum((check_config, check, recover)) > 1:
        raise ValueError('检查与恢复模式不能同时指定')
    if check_config and action not in ('run', 'worker', 'init'):
        raise ValueError('该操作不支持离线检查')
    if check and action not in ('run', 'worker'):
        raise ValueError('该操作不支持依赖检查')
    if recover and (action != 'worker' or kind not in ('account', 'evidence')):
        raise ValueError('恢复仅适用于账号或证据工作进程')
    if action in ('check', 'doctor') and any((check_config, check, recover)):
        raise ValueError('检查动作不能重复指定运行模式')
    if action in ('run', 'check', 'doctor'):
        args = ['server', '-config', str(config)]
        if action != 'run':
            args += ['-check-config' if action == 'check' else '-check']
    elif action == 'init':
        args = ['init', '-config', str(config)]
    elif action == 'worker':
        if kind not in ('card', 'content', 'whispers', 'account', 'evidence'):
            raise ValueError('工作职责无效')
        args = ['worker', '-config', str(config), '-kind', kind]
    elif action in ('migrate', 'permissions', 'opaque-blocks'):
        args = ['migrate', '-config', str(config), 'up' if action == 'migrate' else action]
    else:
        raise ValueError('操作无效')
    if check_config:
        args.append('-check-config')
    elif check:
        args.append('-check')
    elif recover:
        args.append('-recover')
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
    modes = parser.add_mutually_exclusive_group()
    modes.add_argument('--check-config', action='store_true', help='只离线检查配置')
    modes.add_argument('--check', action='store_true', help='装配依赖，不监听或消费任务')
    modes.add_argument('--recover', action='store_true', help='仅恢复账号或证据删除')
    args = parser.parse_args()
    if args.action == 'configure' and any((args.check_config, args.check, args.recover)):
        parser.error('configure不接受检查或恢复参数')
    path = Path(args.config).resolve()
    if args.action == 'configure':
        initialize(path, args.environment)
        return
    # 目录仅创建结构，不清空状态、不生成新密钥；普通启动不执行DDL或初始化账号。
    argv = command(args.action, path, args.kind, args.check_config, args.check, args.recover)
    values = settings.layers(path)
    local = values.get('business', {}).get('runtime', {})
    for key in (('journalfile', 'objectdirectory') if args.action in ('run', 'worker', 'init') and not (args.check_config or args.check) else ()):
        value = local.get(key)
        if value:
            directory = Path(value)
            if not directory.is_absolute():
                directory = path.parent / directory
            if key == 'journalfile':
                directory = directory.parent
            directory.mkdir(mode=0o700, parents=True, exist_ok=True)
    os.execve(argv[0], argv, settings.process_environment())


if __name__ == '__main__':
    try:
        main()
    except (OSError, ValueError, KeyError):
        print('操作未完成，请检查环境YAML、目录权限和构建产物；凭据不会输出。', file=sys.stderr)
        sys.exit(1)
