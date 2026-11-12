CONFIG ?= configs/development.yaml
KIND ?= content
ENVIRONMENT ?= development
PACKAGES ?= ./...
INTEGRATION_SUITE ?= all
.PHONY: run build test fmt fmt-check vet check check-fast lint vuln tidy configure check-config doctor migrate init worker wordlists integration
run:
	python3 scripts/manage.py run --config $(CONFIG)
configure:
	python3 scripts/manage.py configure --config $(CONFIG) --environment $(ENVIRONMENT)
check-config:
	python3 scripts/manage.py check --config $(CONFIG)
doctor:
	python3 scripts/manage.py doctor --config $(CONFIG)
migrate:
	python3 scripts/manage.py migrate --config $(CONFIG)
init:
	python3 scripts/manage.py init --config $(CONFIG)
worker:
	python3 scripts/manage.py worker --config $(CONFIG) --kind $(KIND)
wordlists:
	python3 scripts/init_wordlists.py --config $(CONFIG)
build:
	go build -trimpath -o bin/ ./cmd/...
test:
	go test -race -cover ./...
fmt:
	gofmt -w cmd internal
fmt-check:
	@files="$$(gofmt -l cmd internal)"; if [ -n "$$files" ]; then printf 'Run make fmt to format:\n%s\n' "$$files"; exit 1; fi
vet:
	go vet ./...
check: fmt-check vet test
lint:
	go run honnef.co/go/tools/cmd/staticcheck@v0.8.2 ./...
vuln:
	go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...
check-fast: fmt-check
	go test $(PACKAGES)
tidy:
	go mod tidy
# 显式临时原生PG/Redis集成；使用临时目录，结束后清理本轮测试数据。
integration: build
	python3 scripts/integration.py --suite $(INTEGRATION_SUITE)
