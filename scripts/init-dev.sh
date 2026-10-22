#!/bin/sh
# 显式初始化忽略目录内或显式外部开发凭据，旧卷缺原口令时拒绝生成。
set -eu
script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
exec python3 "$script_dir/services.py" init-dev "$@"
