# HaalChaal backend. Recipes avoid shell-specific syntax so they work with
# GNU make on Linux, macOS and Windows.

# Load .env if present, and pass every variable to child processes.
-include .env
export

DATABASE_URL ?= postgres://haalchaal:haalchaal_dev@localhost:55432/haalchaal?sslmode=disable
TEST_DATABASE_URL ?= postgres://haalchaal:haalchaal_dev@localhost:55432/haalchaal_test?sslmode=disable
SQLC_IMAGE := sqlc/sqlc:1.29.0

GOBIN := $(subst \,/,$(shell go env GOPATH))/bin
STATICCHECK := $(firstword $(wildcard $(GOBIN)/staticcheck $(GOBIN)/staticcheck.exe))

.PHONY: dev db-up db-down test-db test test-integration lint migrate-up migrate-down migrate-status sqlc genkey simcall

## dev: start Postgres, apply migrations, run the server with fake vendors (worker arrives in M2)
dev: db-up migrate-up
	go run ./cmd/haalchaal serve

db-up:
	docker compose up -d --wait db

db-down:
	docker compose down

## test-db: create the haalchaal_test database used by integration tests
test-db: db-up
	docker compose exec -T db sh -c "createdb -U haalchaal haalchaal_test 2>/dev/null || true"

## test: unit tests, no network
test:
	go test ./...

## test-integration: needs TEST_DATABASE_URL
test-integration:
	$(if $(TEST_DATABASE_URL),,$(error TEST_DATABASE_URL is not set))
	go test -tags=integration -count=1 -p 1 ./...

## lint: go vet, plus staticcheck if installed
lint:
	go vet ./...
ifneq ($(STATICCHECK),)
	$(STATICCHECK) ./...
else
	@echo staticcheck not installed, skipped. Install: go install honnef.co/go/tools/cmd/staticcheck@latest
endif

migrate-up:
	go run ./cmd/haalchaal migrate up

## migrate-down: roll back the most recent migration
migrate-down:
	go run ./cmd/haalchaal migrate down

migrate-status:
	go run ./cmd/haalchaal migrate status

## sqlc: regenerate internal/db from queries/ (runs in Docker; sqlc needs cgo)
sqlc:
	docker run --rm -v "$(CURDIR):/src" -w /src $(SQLC_IMAGE) generate

## genkey: print a new ENCRYPTION_KEYS entry (pass KID=2 for a rotation key)
genkey:
	go run ./cmd/haalchaal genkey $(or $(KID),1)

simcall:
	$(error simcall arrives in Milestone 3)
