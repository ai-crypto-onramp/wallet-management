.PHONY: build test test-migration run lint docker-build docker-run clean migrate-up migrate-down migrate-new compose-up compose-down

# Database connection (overridable via env)
DB_URL ?= postgres://wallet:wallet@localhost:5432/wallet?sslmode=disable

# golang-migrate binary; install with: go install -tags 'postgres' github.com/golang-migrate/migrate/v4/cmd/migrate@latest
MIGRATE_BIN ?= migrate
MIGRATIONS_DIR ?= migrations

build:
	go build -o bin/server .

test:
	go test ./... -race -coverprofile=coverage.out -coverpkg=./...

# Migration round-trip smoke test (requires PostgreSQL at DB_URL)
test-migration:
	go test -tags=migration -run TestMigrationRoundTrip ./internal/storage/postgres/...

run:
	go run .

lint:
	go vet ./...

docker-build:
	docker build -t ai-crypto-onramp/wallet-management .

docker-run:
	docker run --rm -p 8080:8080 ai-crypto-onramp/wallet-management

migrate-up:
	$(MIGRATE_BIN) -path $(MIGRATIONS_DIR) -database "$(DB_URL)" up

# golang-migrate prompts when dropping ALL migrations; auto-confirm.
migrate-down:
	printf 'y\n' | $(MIGRATE_BIN) -path $(MIGRATIONS_DIR) -database "$(DB_URL)" down

# Create a new migration: make migrate-new name=add_foo
migrate-new:
	@test -n "$(name)" || { echo "usage: make migrate-new name=add_foo"; exit 1; }
	$(MIGRATE_BIN) create -ext sql -dir $(MIGRATIONS_DIR) -seq $(name)

compose-up:
	docker compose up -d

compose-down:
	docker compose down -v

clean:
	rm -rf bin/ coverage.out
