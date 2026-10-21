#!/bin/sh
# T004/REQ-020：进程内随机测试凭据，不写入文件、不打印展开后的Compose配置。
set -eu
: "${LIYUANCI_TEST_DB_PASSWORD:=$(openssl rand -hex 24)}"
export LIYUANCI_TEST_DB_PASSWORD
# T004/REQ-018、020：未显式指定项目时使用全新项目，避免随机口令与旧PG卷不匹配。
# 不修改旧卷；完成/失败均停止本次容器，保留卷。调用方指定项目时自行维护凭据与生命周期。
if [ -z "${COMPOSE_PROJECT_NAME:-}" ]; then
  COMPOSE_PROJECT_NAME="liyuanci-t004-$(date +%Y%m%d%H%M%S)-$(openssl rand -hex 3)"
  export COMPOSE_PROJECT_NAME
  cleanup() {
    integration_status=$?
    # 仅输出本次应用和MQ技术日志，不展开环境/DSN；失败证据先保留，再清理容器。
    docker compose -f compose.integration.yaml stop api worker || true
    docker compose -f compose.integration.yaml logs --no-color api worker broker namesrv mq-init || true
    if ! docker compose -f compose.integration.yaml down; then
      printf 'T004 cleanup failed\n' >&2
      exit 1
    fi
    exit "$integration_status"
  }
  trap cleanup EXIT
fi
printf 'T004/REQ-020 integration project: %s\n' "$COMPOSE_PROJECT_NAME"
make integration
