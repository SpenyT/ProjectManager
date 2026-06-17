.PHONY: bench-init bench-main bench-prev bench-local lint test test-cover build \
        db-up db-down db-reset migrate-up migrate-down migrate-create

build:
	go build ./...

lint:
	golangci-lint run

RACE := $(shell command -v gcc >/dev/null 2>&1 && echo -race)

test:
	go test $(RACE) -shuffle=on -timeout=120s ./...

test-cover:
	go test $(RACE) -shuffle=on -timeout=120s -coverprofile=coverage.out -covermode=atomic ./...
	go tool cover -func=coverage.out | tail -n 1


-include .env
export

db-up:
	docker compose up -d db db_test

db-down:
	docker compose down

db-reset:
	docker compose down -v
	docker compose up -d db db_test

# Wait for dev DB to be healthy before running migrations.
migrate-up:
	docker compose up -d db
	docker compose exec db sh -c 'until pg_isready -U $$POSTGRES_USER -d $$POSTGRES_DB; do sleep 1; done'
	go run -tags 'postgres' -mod=mod github.com/golang-migrate/migrate/v4/cmd/migrate \
		-path db/migrations \
		-database "$(DATABASE_URL)" \
		up

migrate-down:
	go run -tags 'postgres' -mod=mod github.com/golang-migrate/migrate/v4/cmd/migrate \
		-path db/migrations \
		-database "$(DATABASE_URL)" \
		down 1

migrate-create:
	go run -tags 'postgres' -mod=mod github.com/golang-migrate/migrate/v4/cmd/migrate \
		create -ext sql -dir db/migrations -seq $(name)


bench-init:
	@echo "--- Initializing local baseline from main ---"
	@mkdir -p .benchmarks
	@command -v benchstat >/dev/null || go install golang.org/x/perf/cmd/benchstat@latest
	git stash --include-untracked --quiet || true
	git checkout main
	go test -bench=. -benchmem -run='^$$' -count=5 ./... > .benchmarks/local.txt
	git checkout -
	git stash pop --quiet || true

# Mode 1: compare vs main
bench-main:
	./scripts/bench.sh main

# Mode 2: compare vs last commit
bench-prev:
	./scripts/bench.sh prev

# Mode 3: compare vs saved local baseline
bench-local:
	./scripts/bench.sh local
