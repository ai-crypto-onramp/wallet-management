.PHONY: build test test-race test-integration lint coverage run \
	migrate-up migrate-down migrate-new \
	docker-build docker-run docker-up docker-down clean proto

# Regenerate Go stubs from proto/wallet.proto via buf.
# Requires buf + protoc-gen-go + protoc-gen-go-grpc on PATH (run
# `go install github.com/bufbuild/buf/cmd/buf@latest \
#	google.golang.org/protobuf/cmd/protoc-gen-go@latest \
#	google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest`).
proto:
	buf generate

build:
	go build -o bin/server ./cmd/wallet-management

test:
	go test ./cmd/... ./internal/... -race -timeout 120s -coverprofile=coverage.out -coverpkg=./cmd/...,./internal/...

test-race:
	go test ./cmd/... ./internal/... -race -timeout 180s -coverprofile=coverage.out -coverpkg=./cmd/...,./internal/...

test-integration:
	go test ./cmd/... ./internal/... -race -tags=integration -timeout 300s -coverprofile=coverage.out -coverpkg=./cmd/...,./internal/...

lint:
	golangci-lint run

coverage: test-race
	go tool cover -func=coverage.out | tail -1

run:
	go run ./cmd/wallet-management

migrate-up:
	go run ./cmd/migrate -direction up

migrate-down:
	go run ./cmd/migrate -direction down

migrate-new:
	@test -n "$(NAME)" || (echo "usage: make migrate-new NAME=add_widgets" && exit 1)
	@next=$$(printf '%04d' $$(( $$(ls migrations/*.up.sql 2>/dev/null | wc -l | tr -d ' ') + 1 ))); \
	touch migrations/$${next}_$(NAME).up.sql migrations/$${next}_$(NAME).down.sql; \
	echo "created migrations/$${next}_$(NAME).{up,down}.sql"

docker-build:
	docker build -t ai-crypto-onramp/wallet-management .

docker-run:
	docker run --rm -p 8080:8080 -p 9090:9090 ai-crypto-onramp/wallet-management

docker-up:
	docker compose up -d --wait

docker-down:
	docker compose down

clean:
	rm -rf bin/ coverage.out
