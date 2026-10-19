#!/bin/sh
# T004/REQ-018、020：固定入口目录，不source环境文件、不打印凭据。
set -eu
script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
exec python3 "$script_dir/services.py" start "$@"
