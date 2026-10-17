.PHONY: run build test fmt fmt-check vet check tidy
run:
	go run ./cmd/server
build:
	go build -trimpath -o bin/server ./cmd/server
	go build -trimpath -o bin/worker ./cmd/worker
	go build -trimpath -o bin/probe ./cmd/probe
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

# T004/REQ-020：真实集成与单元检查分开，不因缺少Docker跳过后声称通过。
integration:
	docker compose -f compose.integration.yaml up -d --wait postgres redis namesrv broker
	docker compose -f compose.integration.yaml run --rm migrate
	# T004/REQ-020：订阅前显式建立探针主题，不能依赖首次生产才自动创建。
	docker compose -f compose.integration.yaml exec -T broker sh mqadmin updateTopic -n namesrv:9876 -c DefaultCluster -t liyuanci_foundation_probe_v1
	docker compose -f compose.integration.yaml up -d --build --wait api worker
	docker compose -f compose.integration.yaml run --rm probe
	python3 scripts/check-container-api.py
