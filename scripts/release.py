#!/usr/bin/env python3
"""离线核对生产制品和分进程配置；可选无网络容器诊断，不部署、不迁移、不发布。"""
import argparse
import json
from pathlib import Path
import re
import subprocess
import sys

from configuration import ROOT
from services import load_env, OperationError


PROCESSES = {
    "api": "API", "card": "CARD", "content": "CONTENT", "whispers": "WHISPERS",
    "account": "PRIVACY", "evidence": "EVIDENCE", "bootstrap": "INIT",
}
SHARED = (
    "Enabled", "GatewayURL", "ProviderVersion", "KeyRefs", "RecoveryKeyRefs",
    "ReceiptKey", "CursorKey", "RateKey", "GovernanceKey", "PrivacyKey", "EvidenceKey", "SafetyKey",
)
BUSINESS_SHARED = ("SessionKey", "CardKey", "VerificationKey", "WishKey", "AdminKey", "CrisisKey", "LookupKeyRefs")


def inventory(environment):
    """检查跨进程共享用途一致及私有目录隔离，不输出配置正文或凭据。"""
    image = environment.get("LIYUANCI_BUSINESS_IMAGE", "")
    if not re.fullmatch(r"[^\s]+@sha256:[0-9a-f]{64}", image):
        raise OperationError("业务镜像必须固定为仓库地址@sha256:64位摘要")
    return image, inventory_directories(environment)


def inventory_directories(environment):
    """容器和原生发布共用私有目录与跨进程用途核对。"""
    directories, configs = {}, {}
    for kind, suffix in PROCESSES.items():
        value = environment.get("LIYUANCI_BUSINESS_" + suffix + "_DIR", "")
        directory = Path(value)
        if not value or not directory.is_absolute() or not directory.is_dir():
            raise OperationError(kind + "缺少绝对路径私有配置目录")
        directory = directory.resolve()
        if any(directory == prior or directory in prior.parents or prior in directory.parents
               for prior in directories.values()):
            raise OperationError("不同进程的私有凭据目录不得重叠")
        try:
            config = json.loads((directory / "runtime.json").read_text())
            runtime = config["Runtime"]
            if not isinstance(runtime, dict):
                raise ValueError()
            if runtime.get("LocalTest") or runtime.get("DatabasePolling"):
                raise OperationError(kind + "不能使用本地适配或数据库轮询")
        except (OSError, ValueError, KeyError, TypeError):
            raise OperationError(kind + "业务JSON不可读或格式错误") from None
        directories[kind], configs[kind] = directory, config
    baseline = configs["api"]["Runtime"]
    for kind, config in configs.items():
        if config["Runtime"].get("TaskTransport", "rocketmq") != baseline.get("TaskTransport", "rocketmq"):
            raise OperationError(kind + "任务通道不一致")
        for field in SHARED:
            if field not in baseline or config["Runtime"].get(field) != baseline[field]:
                raise OperationError(kind + "共享运行约束不一致：" + field)
        for field in BUSINESS_SHARED:
            if config.get(field) and config[field] != configs["api"].get(field):
                raise OperationError(kind + "共享业务用途不一致：" + field)
    return directories


def validate_containers(image, directories, execute=subprocess.run):
    """仅运行已存在镜像的离线配置校验；无网络、只读挂载且绝不自动拉取。"""
    for kind, directory in directories.items():
        command = ["docker", "run", "--rm", "--pull=never", "--network", "none", "--read-only",
                   "--user", "10001:10001", "--cap-drop", "ALL", "--security-opt", "no-new-privileges:true",
                   "--mount", "type=bind,src=" + str(directory) + ",dst=/run/business,readonly"]
        if kind == "api":
            command += ["--entrypoint", "/app/server", image, "-config", "/run/business/app.yaml", "-check-config"]
        elif kind == "bootstrap":
            command += ["--entrypoint", "/app/business-init", image, "-config", "/run/business/app.yaml", "-check-config"]
        else:
            command += ["--entrypoint", "/app/business-worker", image, "-config", "/run/business/runtime.json",
                        "-environment", "production", "-kind", kind, "-check-config"]
        try:
            result = execute(command, cwd=ROOT, capture_output=True, text=True, timeout=60)
        except (OSError, subprocess.SubprocessError):
            raise OperationError(kind + "离线容器配置校验未完成") from None
        if result.returncode != 0:
            raise OperationError(kind + "离线容器配置校验失败；未输出私有配置")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--env-file", required=True)
    parser.add_argument("--validate-containers", action="store_true")
    args = parser.parse_args()
    environment = load_env(args.env_file, {})
    image, directories = inventory(environment)
    if args.validate_containers:
        validate_containers(image, directories)
    print(json.dumps({"result": "success", "processes": list(directories),
                      "offline_containers": args.validate_containers,
                      "scope": "仅制品/配置检查，未连接供应商、未部署、未验收生产"}, ensure_ascii=False))


if __name__ == "__main__":
    try:
        main()
    except (OperationError, OSError, ValueError) as exc:
        print(str(exc) if isinstance(exc, OperationError) else "发布配置读取失败", file=sys.stderr)
        sys.exit(1)
