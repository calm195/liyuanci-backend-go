"""宿主开发共用配置；初始化凭据作为唯一来源，宿主配置只转换连接地址及对象目录。"""
import json
import os
from pathlib import Path
import re
import tempfile

from services import OperationError, load_env
from configuration import private_path


def native(env):
    """显式选择宿主原生PG/Redis，配置缺省保持既有Docker模式。"""
    mode = env.get('LIYUANCI_LOCAL_MIDDLEWARE', 'docker')
    if mode not in ('docker', 'native'):
        raise OperationError('中间件模式必须为docker或native')
    return mode == 'native'


def native_database_environment(env):
    """仅为显式初始化提供本地数据库管理连接，不把口令放入命令行。"""
    import getpass
    result = {key: value for key, value in env.items() if not key.startswith('PG')}
    result.update(PGHOST='127.0.0.1', PGPORT=port(env, 'PG', 5432),
                  PGUSER=env.get('LIYUANCI_NATIVE_PG_USER', getpass.getuser()),
                  PGDATABASE='postgres', PGCONNECT_TIMEOUT='5', PGPASSFILE='/dev/null')
    filename = env.get('LIYUANCI_NATIVE_PG_PASSWORD_FILE')
    if filename:
        result['PGPASSWORD'] = private_path(filename).read_text().strip()
    return result


def host_dsn(dsn, env):
    """只转换初始化器生成的DSN，拒绝隐式接入其他数据库。"""
    if not re.fullmatch(r'host=postgres user=dev_[a-z_]+ password=[0-9a-f]{48} dbname=liyuanci_business sslmode=disable', dsn):
        raise OperationError('开发连接格式不匹配，请恢复初始化配置')
    return dsn.replace('host=postgres ', 'host=127.0.0.1 port=' + port(env, 'PG', 5432 if native(env) else 15432) + ' ', 1)


def port(env, name, default):
    """限定宿主端口范围，避免环境文件变成额外连接参数。"""
    value = str(env.get('LIYUANCI_LOCAL_' + name + '_PORT', default))
    if not value.isdecimal() or not 1 <= int(value) <= 65535:
        raise OperationError('宿主端口无效：' + name)
    return str(int(value))


def prepare(entry, env):
    """刷新派生配置，保持密钥、身份和账号不变，API与worker共用宿主对象目录。"""
    entry = private_path(entry)
    source = private_path(env.get('LIYUANCI_DEV_BUSINESS_FILE', entry.parent / 'business.json'))
    cfg = json.loads(source.read_text())
    for field in ('StudentDSN', 'ReviewDSN', 'WishDSN', 'AdminDSN', 'WorkerDSN'):
        cfg[field] = host_dsn(cfg[field], env)
    directory = entry.parent / 'objects'
    directory.mkdir(mode=0o700, exist_ok=True)
    cfg['ObjectDirectory'] = str(directory)
    target = entry.parent / 'business.host.json'
    fd, filename = tempfile.mkstemp(prefix='business-host-', dir=entry.parent)
    pending = Path(filename)
    # 原始配置和密钥仍归初始化器管理；此文件可安全重新生成。
    with os.fdopen(fd, 'w') as output:
        json.dump(cfg, output, indent=2)
    pending.chmod(0o600)
    pending.replace(target)
    return target


def environment(entry):
    """统一读取字面配置，清除终端遗留的APP覆盖，保证普通开发使用所选隔离资源。"""
    env = load_env(private_path(entry), dict(os.environ))
    result = {key: value for key, value in env.items() if not key.startswith('APP_')}
    if native(result):
        result = {key: value for key, value in result.items() if not key.startswith('PG')}
    return result


def app_environment(entry, env):
    """装配宿主进程环境；数据库DSN由业务文件覆盖，MQ默认不构造客户端。"""
    result = env.copy()
    result.update(APP_ENVIRONMENT='integration', APP_DEVAPPFILE=str(prepare(entry, env)),
                  APP_DATABASE_DRIVER='postgres', APP_DATABASE_DSN='devapp',
                  APP_REDIS_ENABLED='true', APP_REDIS_ADDRESS='127.0.0.1:' + port(env, 'REDIS', 6379 if native(env) else 16379),
                  APP_ROCKETMQ_ENABLED='false',
                  APP_SERVER_ADDRESS='127.0.0.1:' + port(env, 'API', 18080))
    # 单独指定领域消息配置；保留清除终端APP覆盖的边界，不隐式复用探针客户端。
    if env.get('LIYUANCI_LOCAL_CARD_MQ_FILE'):
        result['APP_CARD_MQFILE'] = str(private_path(env['LIYUANCI_LOCAL_CARD_MQ_FILE']))
    if env.get('LIYUANCI_LOCAL_CONTENT_MQ_FILE'):
        result['APP_CONTENT_MQFILE'] = str(private_path(env['LIYUANCI_LOCAL_CONTENT_MQ_FILE']))
    if env.get('LIYUANCI_LOCAL_CONTENT_ANALYZER'):
        result['APP_CONTENT_ANALYZER'] = env['LIYUANCI_LOCAL_CONTENT_ANALYZER']
    if native(env) and env.get('LIYUANCI_NATIVE_REDIS_PASSWORD_FILE'):
        result['APP_REDIS_PASSWORD'] = private_path(env['LIYUANCI_NATIVE_REDIS_PASSWORD_FILE']).read_text().strip()
    return result
