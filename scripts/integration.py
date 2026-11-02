#!/usr/bin/env python3
"""在一次性原生PostgreSQL/Redis实例中验证单DSN完整业务；结束后删除本轮数据，不创建备份。"""
import os
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


def executable(name):
    found = shutil.which(name)
    if found: return found
    for folder in [Path('/opt/homebrew/opt/postgresql@18/bin'), *sorted(Path('/usr/lib/postgresql').glob('*/bin'), reverse=True)]:
        candidate = folder / name
        if candidate.is_file(): return str(candidate)
    raise ValueError('缺少原生测试依赖：' + name)


def port():
    with socket.socket() as sock:
        sock.bind(('127.0.0.1', 0))
        return sock.getsockname()[1]


def run():
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
            subprocess.run([postgres['initdb'], '-D', str(pgdata), '-U', 'test_admin', '--auth-local=trust', '--auth-host=scram-sha-256', '--pwfile', str(pw)], env=env, check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            pw.unlink()
            subprocess.run([postgres['pg_ctl'], '-D', str(pgdata), '-l', str(root/'postgres.log'), '-o', '-h 127.0.0.1 -p '+str(pgport)+' -k '+str(root), '-w', 'start'],env=env,check=True,stdout=subprocess.DEVNULL)
            started=True
            subprocess.run([postgres['psql'], '-h', str(root), '-p', str(pgport), '-U', 'test_admin', '-d', 'postgres', '-v', 'ON_ERROR_STOP=1', '-c', 'CREATE DATABASE liyuanci'],env=env,check=True,stdout=subprocess.DEVNULL)
            for number,password in [(redisport,''),(authport,redispassword)]:
                # Redis原生输入由本次测试参数生成，stdio传递，不接受环境覆盖。
                proc=subprocess.Popen([redis,'-'],stdin=subprocess.PIPE,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL,env=env)
                proc.stdin.write(('bind 127.0.0.1\nport '+str(number)+'\nsave ""\nappendonly no\n'+('requirepass '+password+'\n' if password else '')).encode())
                proc.stdin.close()
                children.append(proc)
            deadline=time.monotonic()+10
            for number in (redisport,authport):
                while True:
                    try:
                        with socket.create_connection(('127.0.0.1',number),timeout=0.2):break
                    except OSError:
                        if time.monotonic()>deadline:raise ValueError('测试Redis启动失败')
                        time.sleep(0.05)
            dsn=f'host=127.0.0.1 port={pgport} user=test_admin password={dbpassword} dbname=liyuanci sslmode=disable '
            values={'whisper_text_test_dsn':dsn,'whisper_redis_test_address':f'127.0.0.1:{redisport}','business_runtime_redis_address':f'127.0.0.1:{authport}','business_runtime_redis_password':redispassword,
                    'whisper_server_binary':str(ROOT/'bin/server'),'business_runtime_worker_binary':str(ROOT/'bin/business-worker'),'business_runtime_init_binary':str(ROOT/'bin/business-init'),'whisper_content_binary':str(ROOT/'bin/content-worker'),'whisper_privacy_binary':str(ROOT/'bin/privacy-worker')}
            config=root/'tests.yaml'
            yamlio.write(config,values)
            result=subprocess.run(['go','test','./internal/whisperapp','-count=1','-timeout=8m','-v','-run','^TestPostgres(LocalBusiness|WhisperRuntime|BusinessDatabaseRuntime)$','-args','-integration-config',str(config)],cwd=ROOT,env=env)
            if result.returncode:raise ValueError('原生集成失败')
        finally:
            for proc in children:
                proc.terminate()
                proc.wait(timeout=10)
            if started:
                subprocess.run([postgres['pg_ctl'],'-D',str(pgdata),'-m','fast','-w','stop'],env=env,check=True,stdout=subprocess.DEVNULL)
    print('原生集成完成，本轮PG/Redis及临时数据已清理。')


if __name__ == '__main__':
    try: run()
    except (OSError,ValueError,subprocess.SubprocessError):
        print('原生集成未完成，请核对依赖和测试输出；未操作已有数据库。',file=sys.stderr)
        sys.exit(1)
