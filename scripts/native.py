#!/usr/bin/env python3
"""原生生产制品、配置模板及本机systemd入口；不连接SSH、不安装包、不初始化数据。"""
import argparse
import grp
import hashlib
import json
import os
from pathlib import Path
import platform
import shutil
import stat
import subprocess
import sys

sys.dont_write_bytecode = True

from configuration import ROOT
from release import PROCESSES, inventory_directories
from services import OperationError

CONFIG = Path("/etc/liyuanci/native")
CURRENT = Path("/opt/liyuanci/current")
KINDS = ("api", "card", "content", "whispers", "account", "evidence")
UNITS = tuple("liyuanci-" + kind + ".service" for kind in KINDS)
INFRA = ("liyuanci-postgres.service", "liyuanci-redis.service")


def run(command, timeout=120, **kwargs):
    """错误不输出子进程原文，避免配置路径后的驱动错误泄露凭据。"""
    try:
        result = subprocess.run(command, check=False, capture_output=True, text=True,
                                timeout=timeout, **kwargs)
    except (OSError, subprocess.SubprocessError):
        raise OperationError("原生操作未完成：" + Path(command[0]).name) from None
    if result.returncode:
        raise OperationError("原生操作失败：" + Path(command[0]).name)
    return result.stdout


def empty_destination(path):
    path = Path(path).absolute()
    if path.exists() or path.is_symlink():
        raise OperationError("目标必须不存在；禁止覆盖已有制品、配置或历史密钥")
    path.mkdir(parents=True, mode=0o700)
    return path


def write(path, content, mode=0o600):
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    with path.open("x") as file:
        file.write(content)
    path.chmod(mode)


def scaffold(destination):
    """生成空凭据模板；每个进程只保留所属业务身份，不生成替代历史密钥。"""
    dest = empty_destination(destination)
    source = json.loads((ROOT / "configs/examples/business-runtime.json").read_text())
    dsns = {
        "api": ("APIDSN", "ReviewDSN", "CrisisDSN", "PrivacyDSN", "EvidenceDeletionDSN"),
        "card": ("WorkerDSN",), "content": ("WorkerDSN",), "whispers": ("WorkerDSN",),
        "account": ("PrivacyDSN",), "evidence": ("EvidencePurgerDSN",),
        "bootstrap": ("ReviewDSN",),
    }
    for kind in PROCESSES:
        cfg = json.loads(json.dumps(source))
        runtime = cfg["Runtime"]
        runtime.update(TaskTransport="postgres", DatabasePolling=False,
                       DatabaseMaxOpen=2, DatabaseMaxIdle=1, ReviewMQFile="", UnlockMQFile="")
        cfg.update(CardMQFile="", ContentMQFile="")
        cfg["Card"]["DecodeSlots"] = 1
        for key in list(runtime):
            if key.endswith("DSN") and key not in dsns[kind] and not key.startswith("Recovery"):
                del runtime[key]
        if kind not in ("api", "bootstrap"):
            for key in ("StudentDSN", "AdminDSN", "AdminKey", "AdminPasswordFile", "LookupKeyRefs"):
                cfg.pop(key, None)
        if kind != "api":
            for key in ("SessionKey", "VerificationKey", "WishKey", "CrisisKey", "WeChatAppID", "WeChatSecretFile"):
                cfg.pop(key, None)
        if kind not in ("api", "card"):
            cfg.pop("CardKey", None)
        content = json.dumps(cfg, ensure_ascii=False, indent=2).replace("/run/business/", str(CONFIG / kind) + "/") + "\n"
        write(dest / kind / "runtime.json", content)
        if kind in ("api", "bootstrap"):
            app = (ROOT / "configs/native/app.yaml").read_text().replace("@PROCESS@", kind)
            write(dest / kind / "app.yaml", app)
    shutil.copytree(ROOT / "configs/native/postgres", dest / "postgres")
    shutil.copytree(ROOT / "configs/native/redis", dest / "redis")
    write(dest / "monitor/monitor.env", (ROOT / "configs/native/monitor.env").read_text())
    for path in dest.rglob("*"):
        path.chmod(0o700 if path.is_dir() else 0o600)


def bundle(destination, arch):
    """构建Linux静态Go制品及公开运维文件；清单只含源版本和校验和。"""
    dest = empty_destination(destination)
    (dest / "bin").mkdir(mode=0o755)
    environment = dict(os.environ, CGO_ENABLED="0", GOOS="linux", GOARCH=arch)
    for binary in ("server", "business-worker", "business-init", "migrate"):
        run(["go", "build", "-trimpath", "-o", str(dest / "bin" / binary), "./cmd/" + binary],
            timeout=600, cwd=ROOT, env=environment)
    shutil.copytree(ROOT / "deploy/systemd", dest / "systemd")
    shutil.copytree(ROOT / "configs/native", dest / "templates")
    (dest / "scripts").mkdir()
    for name in ("native.py", "release.py", "configuration.py", "services.py", "native-health.sh"):
        shutil.copy2(ROOT / "scripts" / name, dest / "scripts" / name)
    # scaffold使用的公共模板随制品提供，不带任何.local或运行凭据。
    (dest / "configs/examples").mkdir(parents=True)
    shutil.copy2(ROOT / "configs/examples/business-runtime.json", dest / "configs/examples")
    shutil.copytree(ROOT / "configs/native", dest / "configs/native")
    head = run(["git", "rev-parse", "HEAD"], cwd=ROOT).strip()
    dirty = bool(run(["git", "status", "--porcelain"], cwd=ROOT).strip())
    files = {str(p.relative_to(dest)): hashlib.sha256(p.read_bytes()).hexdigest()
             for p in sorted(dest.rglob("*")) if p.is_file()}
    write(dest / "manifest.json", json.dumps(dict(source=head, dirty=dirty, platform="linux/" + arch,
                                                 files=files), indent=2) + "\n", 0o644)
    for path in dest.rglob("*"):
        path.chmod(0o755 if path.is_dir() or path.parent == dest / "bin" else 0o644)
    dest.chmod(0o755)


def verify_bundle(path):
    root = Path(path).resolve()
    manifest = json.loads((root / "manifest.json").read_text())
    expected = manifest["files"]
    actual = {str(p.relative_to(root)) for p in root.rglob("*") if p.is_file() and p != root / "manifest.json"}
    if set(expected) != actual or any(p.is_symlink() for p in root.rglob("*")):
        raise OperationError("制品文件集合或符号链接不符合清单")
    for name, digest in expected.items():
        relative = Path(name)
        if relative.is_absolute() or ".." in relative.parts or hashlib.sha256((root / relative).read_bytes()).hexdigest() != digest:
            raise OperationError("制品摘要不匹配")
    if not {"bin/server", "bin/business-worker", "bin/business-init", "bin/migrate"}.issubset(expected):
        raise OperationError("缺少生产二进制")
    return manifest


def check_private_directory(directory, gid, uid=0):
    """私有目录只允许运维与所属进程读取，拒绝软链接和跨进程可读配置。"""
    directory = Path(directory)
    for path in (directory, *directory.rglob("*")):
        info = path.lstat()
        expected = 0o750 if stat.S_ISDIR(info.st_mode) else 0o640
        if (not stat.S_ISDIR(info.st_mode) and not stat.S_ISREG(info.st_mode)
                or info.st_uid != uid or info.st_gid != gid or stat.S_IMODE(info.st_mode) != expected):
            raise OperationError("私有配置权限或文件类型不符合进程隔离约束")


def check_config():
    """目标Linux上以各自OS身份执行离线检查，不使用迁移账号或连接供应商。"""
    directories = inventory_directories({"LIYUANCI_BUSINESS_" + suffix + "_DIR": str(CONFIG / kind)
                                         for kind, suffix in PROCESSES.items()})
    for kind, directory in directories.items():
        if (CONFIG / kind).is_symlink():
            raise OperationError("进程私有目录不得使用符号链接")
        check_private_directory(directory, grp.getgrnam("lyc-" + kind).gr_gid)
        cfg = json.loads((directory / "runtime.json").read_text())
        if cfg["Runtime"].get("TaskTransport") != "postgres":
            raise OperationError("原生精简方案必须显式选择postgres任务通道")
        command = process_command(kind, "-check-config")
        run(["runuser", "-u", "lyc-" + kind, "--", "env", "-i", "PATH=/usr/bin:/bin"] + command)


def process_command(kind, flag):
    if kind in ("api", "bootstrap"):
        return [str(CURRENT / "bin" / ("server" if kind == "api" else "business-init")),
                "-config", str(CONFIG / kind / "app.yaml"), flag]
    return [str(CURRENT / "bin/business-worker"), "-config", str(CONFIG / kind / "runtime.json"),
            "-environment", "production", "-kind", kind, flag]


def verify_units(units):
    """仅允许管理从当前制品链接的专属单元，防止误操作同名既有服务。"""
    for unit in units:
        fragment = run(["systemctl", "show", "--property=FragmentPath", "--value", unit]).strip()
        if not fragment or Path(fragment).resolve() != (CURRENT / "systemd" / unit).resolve():
            raise OperationError("单元来源不属于当前原生制品：" + unit)


def lifecycle(action, scope, apply):
    units = list(UNITS) + (list(INFRA) if scope == "all" else [])
    if action == "status":
        verify_units(units)
        return run(["systemctl", "show", "--property=Id,ActiveState,SubState,NRestarts,MemoryCurrent", *units])
    target = "liyuanci.target" if scope == "all" else "liyuanci-business.target"
    # 普通停止保留PG/Redis；全停按systemd依赖反序退出，始终保留数据。
    command = ["systemctl", action, target]
    if not apply:
        return json.dumps({"apply": False, "command": command}, ensure_ascii=False)
    # business启动也会经Requires拉起基础设施，须同时验证所有被间接触发的单元。
    verify_units([*UNITS, *INFRA, "liyuanci.target", "liyuanci-business.target", "liyuanci.slice",
                  "liyuanci-health.timer", "liyuanci-health.service"])
    if action in ("start", "restart"):
        verify_bundle(CURRENT)
        check_config()
    run(command)
    if action in ("start", "restart"):
        run(["systemctl", "is-active", "--quiet", *units])
    return "操作完成；数据和历史密钥保留"


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="action", required=True)
    for name in ("scaffold", "bundle"):
        command = sub.add_parser(name)
        command.add_argument("--output", required=True)
        if name == "bundle":
            command.add_argument("--arch", choices=("amd64", "arm64"), default="amd64")
    command = sub.add_parser("verify")
    command.add_argument("--bundle", required=True)
    sub.add_parser("check-config")
    sub.add_parser("check")
    for name in ("start", "stop", "restart", "status"):
        command = sub.add_parser(name)
        command.add_argument("--scope", choices=("business", "all"), default="business")
        command.add_argument("--apply", action="store_true")
    args = parser.parse_args()
    if args.action == "scaffold":
        scaffold(args.output)
    elif args.action == "bundle":
        bundle(args.output, args.arch)
    elif args.action == "verify":
        manifest = verify_bundle(args.bundle)
        print(json.dumps({key: manifest[key] for key in ("source", "dirty", "platform")}))
    elif args.action in ("check-config", "check"):
        if platform.system() != "Linux" or os.geteuid() != 0:
            raise OperationError("分身份校验需在目标Linux由root显式执行")
        verify_bundle(CURRENT)
        check_config()
        if args.action == "check":
            for kind in KINDS:
                run(["runuser", "-u", "lyc-" + kind, "--", "env", "-i", "PATH=/usr/bin:/bin"] + process_command(kind, "-check"))
            print("依赖检查通过；未消费任务，不等于业务、容量或恢复验收")
    else:
        print(lifecycle(args.action, args.scope, args.apply))


if __name__ == "__main__":
    try:
        main()
    except (OperationError, OSError, ValueError, KeyError, TypeError):
        print("原生操作失败；检查制品、单元归属、权限和私有配置。未输出凭据。", file=sys.stderr)
        sys.exit(1)
