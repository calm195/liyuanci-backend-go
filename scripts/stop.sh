#!/bin/sh
# T004/REQ-018、020：只操作指定项目，默认保留容器/网络/数据卷。
set -eu
script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
exec python3 "$script_dir/services.py" stop "$@"
