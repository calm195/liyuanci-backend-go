.PHONY: run build test fmt fmt-check vet check tidy
run:
	go run ./cmd/server
build:
	go build -trimpath -o bin/server ./cmd/server
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
