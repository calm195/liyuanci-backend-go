"""开发配置路径与旧目录导入；凭据仅放忽略目录，不覆盖已有配置。"""
from pathlib import Path
import os
import shutil
import tempfile

ROOT = Path(__file__).resolve().parents[1]
LOCAL_ROOT = ROOT / '.local'


def private_path(value):
    """允许仓库.local及兼容的仓库外路径，拒绝凭据落入源码目录。"""
    path = Path(value).expanduser().resolve()
    if (path == ROOT or ROOT in path.parents) and LOCAL_ROOT.resolve() not in path.parents and path != LOCAL_ROOT.resolve():
        raise ValueError('仓库内凭据和状态必须位于已忽略的.local目录')
    return path


def directory(mode):
    """返回按运行模式隔离的默认私有目录。"""
    return LOCAL_ROOT / mode


def import_legacy(mode):
    """首次切换路径时复制原配置和对象；完成后原目录保留，已有目标绝不覆盖。"""
    target = directory(mode).resolve()
    source = (Path.home() / '.config/liyuanci' / mode).resolve()
    if target.exists() or not (source / 'operations.env').is_file():
        return False
    for pidfile in source.glob('*.pid'):
        try:
            os.kill(int(pidfile.read_text().strip()), 0)
        except ProcessLookupError:
            continue
        except (ValueError, PermissionError):
            raise ValueError('旧配置包含未确认的进程状态，请先核对并停止旧应用') from None
        raise ValueError('旧应用仍在运行，请先停止后导入，避免对象目录并发写入')
    # 局部导入环境解析器，避免路径模块与运维入口在导入阶段循环依赖。
    from services import load_env
    env = load_env(source / 'operations.env', {})
    LOCAL_ROOT.mkdir(mode=0o700, parents=True, exist_ok=True)
    staging = Path(tempfile.mkdtemp(prefix='.import-', dir=LOCAL_ROOT))
    try:
        shutil.copytree(source, staging, dirs_exist_ok=True,
                        ignore=shutil.ignore_patterns('bin', '*.pid', '*.lock', '*.pending', 'business.host.json'))
        staging.chmod(0o700)
        for key, value in env.items():
            if not key.endswith('_FILE') or not value:
                continue
            original = Path(value)
            if not original.is_file():
                raise ValueError('旧配置引用文件缺失，请恢复原文件后重试导入')
            if original.is_relative_to(source):
                relative = original.relative_to(source)
            else:
                relative = Path('secrets') / original.name
            copied = staging / relative
            if copied.exists() and copied.read_bytes() != original.read_bytes():
                raise ValueError('旧凭据文件名冲突，拒绝覆盖')
            copied.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
            if not copied.exists():
                shutil.copy2(original, copied)
            env[key] = str(target / relative)
        # 保留未知字面选项；只重写文件引用，不执行环境内容。
        (staging / 'operations.env').write_text(''.join(key + '=' + value + '\n' for key, value in env.items()))
        (staging / 'operations.env').chmod(0o600)
        staging.rename(target)
        print('原开发配置已复制到仓库.local目录；原凭据与数据保留。')
        return True
    finally:
        if staging.exists():
            shutil.rmtree(staging)
