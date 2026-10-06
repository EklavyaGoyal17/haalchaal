# HaalChaal backend. Recipes avoid shell-specific syntax so they work with
# GNU make on Linux, macOS and Windows.

# Load .env if present, and pass every variable to child processes.
-include .env
export

DATABASE_URL ?= postgres://haalchaal:haalchaal_dev@localhost:55432/haalchaal?sslmode=disable

GOBIN := $(subst \,/,$(shell go env GOPATH))/bin
STATICCHECK := $(firstword $(wildcard $(GOBIN)/staticcheck $(GOBIN)/staticcheck.exe))

.PHONY: dev db-up db-down test test-integration lint migrate-up migrate-down sqlc simcall

## dev: start Postgres and run the server with fake vendors (worker arrives in M2)
dev: db-up
	go run ./cmd/haalchaal serve

db-up:
	docker compose up -d --wait db

db-down:
	docker compose down

## test: unit tests, no network
test:
	go test ./...

## test-integration: needs TEST_DATABASE_URL
test-integration:
	$(if $(TEST_DATABASE_URL),,$(error TEST_DATABASE_URL is not set))
	go test -tags=integration ./...

## lint: go vet, plus staticcheck if installed
lint:
	go vet ./...
ifneq ($(STATICCHECK),)
	$(STATICCHECK) ./...
else
	@echo staticcheck not installed, skipped. Install: go install honnef.co/go/tools/cmd/staticcheck@latest
endif

migrate-up migrate-down:
	$(error $@ arrives in Milestone 1)

sqlc:
	$(error sqlc arrives in Milestone 1)

simcall:
	$(error simcall arrives in Milestone 3)
