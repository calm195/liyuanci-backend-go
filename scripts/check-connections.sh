#!/bin/sh
# T004/REQ-020：内部网络不向宿主开放中间件端口；在PG容器内执行Go驱动真实连接。
set -eu
: "${LIYUANCI_TEST_DB_PASSWORD:=$(openssl rand -hex 24)}"
export LIYUANCI_TEST_DB_PASSWORD
docker compose -f compose.integration.yaml up -d --wait postgres redis
case "$(docker info --format '{{.Architecture}}')" in
  aarch64|arm64) probe_arch=arm64 ;;
  x86_64|amd64) probe_arch=amd64 ;;
  *) printf 'unsupported integration architecture\n' >&2; exit 1 ;;
esac
CGO_ENABLED=0 GOOS=linux GOARCH="$probe_arch" go build -trimpath -o bin/probe-linux ./cmd/probe
probe_container="$(docker compose -f compose.integration.yaml ps -q postgres)"
docker cp bin/probe-linux "$probe_container:/tmp/liyuanci-probe"
# 仅隔离PG容器本地Unix socket鉴权；不读取/输出容器secret、不测试生产口令。
docker exec --user postgres \
 -e APP_ENVIRONMENT=integration \
 -e APP_DATABASE_DRIVER=postgres \
 -e 'APP_DATABASE_DSN=host=/var/run/postgresql user=liyuanci dbname=liyuanci sslmode=disable' \
 -e APP_REDIS_ENABLED=true -e APP_REDIS_ADDRESS=redis:6379 \
 -e APP_ROCKETMQ_ENABLED=true \
 "$probe_container" /tmp/liyuanci-probe -config '' -connections-only
