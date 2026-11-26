TEST_DB ?= postgres://aperture:aperture@localhost:5432/aperture?sslmode=disable

.PHONY: build test test-unit up down tracegen fmt lint bench bench-micro

build:
	go build -o bin/ ./cmd/...

test-unit: ## tests that need no database (integration tests skip)
	go test -race ./...

test: ## full suite; needs PostgreSQL (make up)
	APERTURE_TEST_DATABASE_URL="$(TEST_DB)" go test -race -count=1 ./...

up:
	docker compose up -d --build --wait

down:
	docker compose down --volumes

tracegen:
	go run ./cmd/tracegen -endpoint http://localhost:4318 -traces 100

fmt:
	gofmt -w .

lint:
	test -z "$$(gofmt -l .)"
	go vet ./...

bench: ## end-to-end ingestion benchmarks; stop the compose stack first (docs/benchmarks.md)
	bench/run.sh $(or $(DURATION),20s)

bench-micro: ## Go microbenchmarks
	go test -run '^$$' -bench . -benchmem -count 3 ./internal/...
