#!/usr/bin/env python3
"""宿主业务开发入口：基础设施按需启动，应用前台运行，停止应用保留中间件。"""
import argparse
import fcntl
import json
import os
from pathlib import Path
import re
import signal
import socket
import subprocess
import sys
import tempfile
import time

from localdev import app_environment, environment, native, port
from configuration import directory, import_legacy, private_path
from services import ROOT, OperationError, Services, parser as services_parser


def service_manager(args, env):
    """复用既有Docker项目归属、完成日志和监控配置。"""
    options = services_parser().parse_args(['start', 'dev', '--project', args.project,
        '--monitor-project', args.project + '-observability', '--without-management', '--without-monitoring'])
    manager = Services(options, environment=env)
    manager.defaults()
    return manager


def compose(args):
    result = ['docker', 'compose', '-p', args.project, '-f', str(ROOT / 'deploy/compose/integration.yaml'),
              '-f', str(ROOT / 'deploy/compose/host-dev.yaml'), '-f', str(ROOT / 'deploy/compose/mq-host.yaml')]
    if getattr(args, 'management', False):
        result += ['-f', str(ROOT / 'deploy/compose/management.yaml'), '-f', str(ROOT / 'deploy/compose/host-management.yaml'), '--profile', 'management']
    return result


def infrastructure(args, env):
    if native(env):
        if args.management or args.monitoring:
            raise OperationError('原生模式仅按需管理Docker MQ，管理工具和监控请使用既有Docker模式')
        for name, default in (('PG', 5432), ('REDIS', 6379)):
            try:
                with socket.create_connection(('127.0.0.1', int(port(env, name, default))), timeout=3):
                    pass
            except OSError:
                raise OperationError('本地' + name + '不可连接，请先自行启动该服务') from None
        if args.mq:
            manager = service_manager(args, env)
            base = ['docker', 'compose', '-p', args.project, '-f', str(ROOT / 'deploy/compose/mq.yaml'), '-f', str(ROOT / 'deploy/compose/mq-host.yaml')]
            manager.call(base + ['config', '--quiet'], '校验原生模式MQ编排')
            # 在启动前登记，部分启动失败后也能显式停止本项目MQ。
            (private_path(args.env_file).parent / 'native-mq.started').touch()
            manager.call(base + ['up', '-d', '--wait', '--wait-timeout', '240', 'namesrv', 'broker'], '启动可选MQ')
        print('本地PG/Redis端口检查通过；账号、权限及业务结构请运行dev-native-doctor。')
        return
    manager = service_manager(args, env)
    manager.env['LIYUANCI_TEST_DB_PASSWORD'] = manager.secret_file('LIYUANCI_TEST_DB_PASSWORD_FILE').read_text().strip()
    selected = ['postgres', 'redis']
    if env.get('LIYUANCI_LOCAL_PRIVACY_ENABLED') == 'true':
        selected += ['recovery-postgres']
    if args.mq:
        selected += ['namesrv', 'broker']
    if args.management:
        for key in ('LIYUANCI_PGADMIN_PASSWORD_FILE', 'LIYUANCI_DASHBOARD_PROPERTIES_FILE'):
            manager.secret_file(key, empty=key.endswith('PROPERTIES_FILE'))
        selected += ['pgadmin', 'redisinsight']
        if args.mq:
            selected += ['rocketmq-dashboard']
    base = compose(args)
    manager.call(base + ['config', '--quiet'], '校验宿主开发编排')
    if args.monitoring:
        manager.secret_file('LIYUANCI_GRAFANA_PASSWORD_FILE')
        manager.call(manager.compose(True) + ['config', '--quiet'], '校验可选监控')
    manager.call(base + ['up', '-d', '--wait', '--wait-timeout', '240'] + selected, '启动所选本机中间件')
    if args.monitoring:
        manager.call(manager.compose(True) + ['up', '-d', '--wait', '--wait-timeout', '120'], '启动可选监控')
        print('监控仅采集Docker项目日志；宿主API/worker日志见各自终端，不宣称已接入Loki。')


def binary(entry, name, env):
    directory = private_path(entry).parent / 'bin'
    directory.mkdir(mode=0o700, exist_ok=True)
    path = directory / name
    # 复用Go编译缓存；先写临时文件再替换，运行中的旧进程不受重编译影响。
    fd, filename = tempfile.mkstemp(prefix=name + '-', dir=directory)
    os.close(fd)
    pending = Path(filename)
    result = subprocess.run(['go', 'build', '-o', str(pending), './cmd/' + name], cwd=ROOT, env=env)
    if result.returncode:
        pending.unlink(missing_ok=True)
        raise OperationError('宿主编译失败')
    pending.replace(path)
    return path


def application(args, env):
    name = {'api': 'server', 'worker': 'card-worker', 'content-worker': 'content-worker',
            'privacy-worker': 'privacy-account-worker', 'evidence-worker': 'privacy-evidence-worker'}[args.action]
    state = private_path(args.env_file).parent
    # 锁覆盖编译、派生配置和进程生存期，拒绝同一配置下重复运行相同应用。
    with (state / (name + '.lock')).open('a') as lock:
        try:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            raise OperationError('同一配置的应用已在运行，请先停止') from None
        app_env = app_environment(args.env_file, env)
        privacy = args.action in ('privacy-worker', 'evidence-worker')
        if privacy:
            app_env['APP_PRIVACY_SCOPE'] = 'account' if args.action == 'privacy-worker' else 'evidence'
        path = binary(args.env_file, 'privacy-worker' if privacy else name, app_env)
        command = [str(path)] + (['-config', ''] if name == 'server' else [])
        child = subprocess.Popen(command, cwd=ROOT, env=app_env, start_new_session=True, pass_fds=(lock.fileno(),))
        pidfile = state / (name + '.pid')
        pidfile.write_text(str(child.pid))
        previous = {}
        def forward(signum, _frame):
            if child.poll() is None:
                child.send_signal(signal.SIGTERM if signum == signal.SIGHUP else signum)
        for signum in (signal.SIGINT, signal.SIGTERM, signal.SIGHUP):
            previous[signum] = signal.signal(signum, forward)
        try:
            code = child.wait()
            if code:
                raise OperationError('宿主应用退出，详情见上方日志')
        finally:
            if child.poll() is None:
                child.terminate()
                child.wait(timeout=30)
            pidfile.unlink(missing_ok=True)
            for signum, handler in previous.items():
                signal.signal(signum, handler)


def stop_apps(entry):
    """只向所选配置记录且命令匹配的宿主进程发TERM，绝不全局按名称杀进程。"""
    state = private_path(entry).parent
    for name in ('server', 'card-worker', 'content-worker', 'privacy-account-worker', 'privacy-evidence-worker'):
        pidfile = state / (name + '.pid')
        if not pidfile.exists():
            continue
        pid = int(pidfile.read_text().strip())
        expected = str(state / 'bin' / ('privacy-worker' if name.startswith('privacy-') else name))
        command = subprocess.run(['ps', '-p', str(pid), '-o', 'command='], text=True, capture_output=True)
        if command.returncode:
            pidfile.unlink(missing_ok=True)
            continue
        if not (command.stdout.strip() == expected or command.stdout.strip().startswith(expected + ' ')):
            raise OperationError('PID归属不匹配，请核对所选开发状态目录')
        try:
            os.kill(pid, signal.SIGTERM)
        except ProcessLookupError:
            pass
        deadline = time.monotonic() + 30
        while pidfile.exists() and time.monotonic() < deadline:
            status = subprocess.run(['ps', '-p', str(pid), '-o', 'stat='], text=True, capture_output=True)
            if status.returncode or status.stdout.strip().startswith('Z'):
                pidfile.unlink(missing_ok=True)
                break
            time.sleep(0.1)
        if pidfile.exists():
            raise OperationError('应用尚未完成退出；保留状态，不强制终止')


def doctor(args, env):
    """诊断真实API装配；缺配置、迁移或权限时失败，不以整体readyz冒充局部诊断。"""
    app_env = app_environment(args.env_file, env)
    path = binary(args.env_file, 'server', app_env)
    result = subprocess.run([str(path), '-config', '', '-check'], cwd=ROOT, env=app_env)
    if result.returncode:
        raise OperationError('开发诊断失败，请核对中间件、迁移及原业务配置')
    print('API与业务worker数据库身份/业务结构、Redis和共享对象目录诊断通过。')
    print('完整业务readyz仍可能503；微信、学校规则及生产能力不属于本次诊断通过范围。')


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('action', choices=['init', 'privacy-init', 'infra', 'api', 'worker', 'content-worker', 'privacy-worker', 'evidence-worker', 'doctor', 'stop', 'infra-stop', 'config', 'mq', 'management', 'monitoring', 'import'])
    parser.add_argument('--native', action='store_true', help='使用已经运行的宿主PG/Redis，Docker仅按需管理MQ')
    parser.add_argument('--project')
    parser.add_argument('--env-file')
    parser.add_argument('--admin-login', default='developer')
    parser.add_argument('--mq', action='store_true')
    parser.add_argument('--management', action='store_true')
    parser.add_argument('--monitoring', action='store_true')
    args = parser.parse_args(argv)
    privacy_init = args.action == 'privacy-init'
    if privacy_init:
        if args.native:
            raise OperationError('隐私初始化当前仅支持Docker独立恢复集群，请显式提供原生恢复资源后装配')
        args.action = 'init'
    if args.action in ('mq', 'management', 'monitoring'):
        setattr(args, args.action, True)
        args.action = 'infra'
    mode = 'native' if args.native else 'local'
    args.project = args.project or ('liyuanci-local-native' if args.native else 'liyuanci-local')
    args.env_file = args.env_file or str(directory(mode) / 'operations.env')
    if args.action in ('init', 'import') and Path(args.env_file) == directory(mode) / 'operations.env':
        import_legacy(mode)
    if args.action == 'import':
        return
    if not re.fullmatch(r'liyuanci-local(?:-[a-z0-9_-]+)?', args.project):
        raise OperationError('宿主入口仅允许liyuanci-local或其带后缀的隔离项目')
    args.env_file = str(private_path(args.env_file))
    if Path(args.env_file).is_file():
        existing = environment(args.env_file)
        if args.native != native(existing):
            raise OperationError('配置模式不匹配，请使用对应的dev-native-*或dev-*入口')
        selected_project = existing.get('LIYUANCI_LOCAL_PROJECT')
        if selected_project and selected_project != args.project:
            raise OperationError('配置目录属于另一宿主项目，请匹配--project与--env-file')
    if args.action == 'init' and args.native:
        entry = private_path(args.env_file)
        entry.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
        if entry.parent.stat().st_mode & 0o077:
            raise OperationError('配置目录权限必须为0700')
        if not entry.exists():
            with entry.open('x') as output:
                output.write('LIYUANCI_LOCAL_MIDDLEWARE=native\nLIYUANCI_LOCAL_PROJECT=' + args.project + '\n')
            entry.chmod(0o600)
        if 'LIYUANCI_LOCAL_PROJECT' not in environment(entry):
            with entry.open('a') as output:
                output.write('\nLIYUANCI_LOCAL_PROJECT=' + args.project + '\n')
        command = [sys.executable, str(ROOT / 'scripts/init-business-dev.py'), '--host', '--native',
                   '--project', args.project, '--env-file', args.env_file, '--admin-login', args.admin_login]
        if subprocess.run(command, cwd=ROOT).returncode:
            raise OperationError('原生PG/Redis业务初始化失败；请核对本地服务、psql及管理员连接配置')
        return
    if args.action == 'init':
        command = [sys.executable, str(ROOT / 'scripts/services.py'), 'init-dev', '--project', args.project,
                   '--monitor-project', args.project + '-observability', '--config-dir', str(Path(args.env_file).parent)]
        if Path(args.env_file).name != 'operations.env':
            raise OperationError('初始化配置文件名必须为operations.env')
        if subprocess.run(command, cwd=ROOT).returncode:
            raise OperationError('开发凭据初始化失败')
        entry = Path(args.env_file)
        if 'LIYUANCI_LOCAL_PROJECT' not in environment(entry):
            with entry.open('a') as output:
                output.write('LIYUANCI_LOCAL_PROJECT=' + args.project + '\n')
        command = [sys.executable, str(ROOT / 'scripts/init-business-dev.py'), '--host', '--project', args.project,
                   '--env-file', args.env_file, '--admin-login', args.admin_login]
        if privacy_init:
            command.append('--privacy')
        if subprocess.run(command, cwd=ROOT).returncode:
            raise OperationError('宿主业务初始化失败')
        return
    if args.action == 'stop':
        stop_apps(args.env_file)
        return
    if args.action == 'infra-stop':
        # 显式停止中间件前先等待所选宿主进程退出，仍保留所有卷。
        stop_apps(args.env_file)
        if args.native:
            marker = private_path(args.env_file).parent / 'native-mq.started'
            if marker.exists():
                manager = service_manager(args, environment(args.env_file))
                base = ['docker', 'compose', '-p', args.project, '-f', str(ROOT / 'deploy/compose/mq.yaml'), '-f', str(ROOT / 'deploy/compose/mq-host.yaml')]
                manager.call(base + ['stop', 'broker', 'namesrv', 'mq-init'], '停止本项目MQ并保留卷')
                marker.unlink()
            return
        options = services_parser().parse_args(['stop', 'dev', '--project', args.project,
                                                '--monitor-project', args.project + '-observability'])
        Services(options).stop()
        return
    env = environment(args.env_file)
    for name, default in (('PG', 15432), ('REDIS', 16379), ('API', 18080), ('NAMESRV', 9876)):
        port(env, name, default)
    if args.action == 'infra':
        infrastructure(args, env)
    elif args.action in ('api', 'worker', 'content-worker', 'privacy-worker', 'evidence-worker'):
        application(args, env)
    elif args.action == 'doctor':
        doctor(args, env)
    else:
        print('宿主业务配置：' + app_environment(args.env_file, env)['APP_DEVAPPFILE'])


if __name__ == '__main__':
    started = time.monotonic()
    result = 'failure'
    try:
        main()
        result = 'success'
    except SystemExit as status:
        result = 'success' if status.code in (None, 0) else 'failure'
        raise
    except (OperationError, OSError, ValueError, KeyError, subprocess.SubprocessError) as error:
        print(str(error) if isinstance(error, OperationError) else '宿主开发操作失败，请核对配置、工具及服务状态', file=sys.stderr)
        sys.exit(1)
    finally:
        print(json.dumps({'msg': '宿主开发操作完成', 'result': result,
                          'duration': round(time.monotonic() - started, 3)}, ensure_ascii=False))
