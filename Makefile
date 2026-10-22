.PHONY: run build test fmt fmt-check vet check tidy integration
run:
	go run ./cmd/server
build:
	go build -trimpath -o bin/card-worker ./cmd/card-worker
	go build -trimpath -o bin/server ./cmd/server
	go build -trimpath -o bin/worker ./cmd/worker
	go build -trimpath -o bin/probe ./cmd/probe
	go build -trimpath -o bin/migrate ./cmd/migrate
	go build -trimpath -o bin/admin-init ./cmd/admin-init
	go build -trimpath -o bin/dev-init ./cmd/dev-init
test:
	go test -race -cover ./...
fmt:
	gofmt -w cmd internal
fmt-check:
	@files="$$(gofmt -l cmd internal)"; if [ -n "$$files" ]; then printf 'Run make fmt to format:\n%s\n' "$$files"; exit 1; fi
vet:
	go vet ./...
check: fmt-check vet test
tidy:
	go mod tidy

# 真实集成与单元检查分开，不因缺少Docker跳过后声称通过。
integration:
	docker compose -f deploy/compose/integration.yaml up -d --wait postgres redis namesrv broker
	docker compose -f deploy/compose/integration.yaml run --rm migrate
	# 订阅前显式建立探针主题，不能依赖首次生产才自动创建。
	docker compose -f deploy/compose/integration.yaml exec -T broker sh mqadmin updateTopic -n namesrv:9876 -c DefaultCluster -t liyuanci_foundation_probe_v1
	docker compose -f deploy/compose/integration.yaml up -d --build --wait api worker
	docker compose -f deploy/compose/integration.yaml run --rm probe
	# 领域主题独立于探针；真实数据库、MQ和业务回执必须在同一隔离项目验证。
	docker compose -f deploy/compose/integration.yaml exec -T broker sh mqadmin updateTopic -n namesrv:9876 -c DefaultCluster -t liyuanci_student_card_scan_v1
	docker compose -f deploy/compose/integration.yaml run --rm --build business-test

# 日常迭代只验证相关包；交付前执行check，CI仍保留全量质量检查。
.PHONY: check-fast
PACKAGES ?= ./...
DEV_ARGS ?=
check-fast: fmt-check
	go test $(PACKAGES)
# 原生模式规则先于通用规则，兼容macOS自带GNU Make 3.81。
dev-native-%:
	python3 scripts/dev.py $* --native $(DEV_ARGS)
dev-%:
	python3 scripts/dev.py $* $(DEV_ARGS)
