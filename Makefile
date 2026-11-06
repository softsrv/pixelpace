APP_NAME         := app
BIN_DIR          := ./bin
MODULE           := github.com/softsrv/starter
SMTP4DEV_NAME    := $(APP_NAME)-smtp4dev
SMTP4DEV_SMTP    := 2525
SMTP4DEV_WEB     := 5000
VALKEY_NAME      := $(APP_NAME)-valkey
VALKEY_PORT      := 6379
DEV_NAME         := $(APP_NAME)-dev
DEV_IMAGE        := $(APP_NAME)-dev
# The one owner of the sqlc version: CI installs it via `make sqlc-install`,
# and sqlc-check refuses any other version (it is recorded in the headers of
# the generated files under internal/db).
SQLC_VERSION     := v1.31.1
# Where `make sqlc-install` puts sqlc (go install: GOBIN, else the first GOPATH
# entry's bin). The sqlc targets run that binary when it exists, so they work
# without GOPATH/bin on PATH (as in CI); otherwise they use `sqlc` on PATH.
SQLC_INSTALL_DIR  = $(or $(shell go env GOBIN),$(firstword $(subst :, ,$(shell go env GOPATH)))/bin)
SQLC              = $(if $(wildcard $(SQLC_INSTALL_DIR)/sqlc),$(SQLC_INSTALL_DIR)/sqlc,sqlc)

# Load .env if present (for local dev convenience)
-include .env
export
# Make-only: keep the bare `export` above from running `go env` for every recipe.
unexport SQLC_INSTALL_DIR SQLC

.PHONY: dev dev-deps dev-container dev-logs dev-stop run build test fmt lint \
        daisyui-install pixi-install tailwind tailwind-watch \
        smtp4dev smtp4dev-stop valkey valkey-stop \
        migrate-up migrate-down migrate-create migrate-status \
        sqlc-install sqlc-generate sqlc-check \
        docker-build docker-run prod clean

## ── Development ─────────────────────────────────────────────────────────────

# Hot reload in Docker: starts smtp4dev and Valkey, applies migrations, then
# runs air in a detached container with the source tree mounted at /src. air
# rebuilds the CSS and the binary on every change. Prints only the container
# ID; follow the output with `make dev-logs` and stop it with `make dev-stop`.
dev: dev-deps
	@$(MAKE) --no-print-directory -s dev-container

# Quiet on success; prints everything if a step fails.
dev-deps:
	@out=$$($(MAKE) --no-print-directory -s smtp4dev valkey migrate-up 2>&1) || { echo "$$out" >&2; exit 1; }

# Inside the container "localhost" is the container itself, so services the
# .env points at on this machine are reached through host.docker.internal.
dockerhost = $(subst 127.0.0.1,host.docker.internal,$(subst localhost,host.docker.internal,$(1)))

# The app reads .env itself, mounted at /config/.env (CONFIG_FILE in
# Dockerfile.dev). Only the host-service addresses are passed as variables,
# rewritten for the container; the environment overrides the file.
DEV_HOST_VARS := DATABASE_URL VALKEY_ADDR SMTP_HOST

dev-container: export DATABASE_URL := $(call dockerhost,$(DATABASE_URL))
dev-container: export VALKEY_ADDR := $(call dockerhost,$(or $(VALKEY_ADDR),localhost:$(VALKEY_PORT)))
dev-container: export SMTP_HOST := $(call dockerhost,$(SMTP_HOST))
dev-container:
	@test -f .env || { echo "no .env: copy .env.example to .env first" >&2; exit 1; }
	@docker build -q -t $(DEV_IMAGE) - < Dockerfile.dev >/dev/null
	@docker rm -fv $(DEV_NAME) >/dev/null 2>&1 || true
	@docker run -d --init --name $(DEV_NAME) \
	  --add-host=host.docker.internal:host-gateway \
	  -p $(or $(PORT),8080):$(or $(PORT),8080) \
	  -v "$(CURDIR)":/src \
	  -v "$(CURDIR)/.env":/config/.env:ro \
	  -v /src/tmp \
	  -v $(APP_NAME)-dev-gomod:/go/pkg/mod \
	  -v $(APP_NAME)-dev-gocache:/root/.cache/go-build \
	  $(addprefix -e ,$(DEV_HOST_VARS)) \
	  $(DEV_IMAGE)

dev-logs:
	docker logs -f $(DEV_NAME)

dev-stop:
	docker rm -fv $(DEV_NAME) 2>/dev/null || true

smtp4dev:
	@if [ -z "$$(docker ps -q -f name=^$(SMTP4DEV_NAME)$$)" ]; then \
	  docker rm -f $(SMTP4DEV_NAME) 2>/dev/null || true; \
	  docker run -d --name $(SMTP4DEV_NAME) \
	    -p $(SMTP4DEV_SMTP):25 \
	    -p $(SMTP4DEV_WEB):80 \
	    rnwood/smtp4dev; \
	  echo "smtp4dev started — SMTP: localhost:$(SMTP4DEV_SMTP)  Web UI: http://localhost:$(SMTP4DEV_WEB)"; \
	else \
	  echo "smtp4dev already running"; \
	fi

smtp4dev-stop:
	docker rm -f $(SMTP4DEV_NAME) 2>/dev/null || true

valkey:
	@if [ -z "$$(docker ps -q -f name=^$(VALKEY_NAME)$$)" ]; then \
	  docker rm -f $(VALKEY_NAME) 2>/dev/null || true; \
	  docker run -d --name $(VALKEY_NAME) \
	    -p $(VALKEY_PORT):6379 \
	    valkey/valkey; \
	  echo "valkey started — localhost:$(VALKEY_PORT)"; \
	else \
	  echo "valkey already running"; \
	fi

valkey-stop:
	docker rm -f $(VALKEY_NAME) 2>/dev/null || true

air:
	air

run:
	go run ./cmd/app

build:
	go build -ldflags="-s -w" -o $(BIN_DIR)/$(APP_NAME) ./cmd/app

## ── Quality ──────────────────────────────────────────────────────────────────

test:
	go test ./...

test-integration:
	go test -tags integration ./...

test-cover:
	go test -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out

fmt:
	gofmt -w .

lint:
	golangci-lint run

## ── CSS ──────────────────────────────────────────────────────────────────────

# Download DaisyUI .mjs bundles next to app.css so the @plugin directive
# can resolve them. These bundles are vendored (checked into git) so a
# fresh clone builds without a network fetch; re-run this whenever you
# want to upgrade DaisyUI and commit the resulting files.
daisyui-install:
	curl -sLo web/static/css/daisyui.mjs \
	  https://github.com/saadeghi/daisyui/releases/latest/download/daisyui.mjs
	curl -sLo web/static/css/daisyui-theme.mjs \
	  https://github.com/saadeghi/daisyui/releases/latest/download/daisyui-theme.mjs
	@echo "DaisyUI bundles downloaded to web/static/css/"

# Download the PixiJS browser bundle used by the race page. Vendored like
# DaisyUI; unsafe-eval.min.js patches PixiJS to run under our CSP, which has no
# 'unsafe-eval'. Bump PIXI_VERSION, re-run, and commit the resulting files.
PIXI_VERSION ?= 8.22.0
pixi-install:
	curl -sfLo web/static/js/vendor/pixi/pixi.min.js \
	  https://cdn.jsdelivr.net/npm/pixi.js@$(PIXI_VERSION)/dist/pixi.min.js
	curl -sfLo web/static/js/vendor/pixi/unsafe-eval.min.js \
	  https://cdn.jsdelivr.net/npm/pixi.js@$(PIXI_VERSION)/dist/packages/unsafe-eval.min.js
	curl -sfLo web/static/js/vendor/pixi/LICENSE \
	  https://cdn.jsdelivr.net/npm/pixi.js@$(PIXI_VERSION)/LICENSE
	@echo "PixiJS $(PIXI_VERSION) downloaded to web/static/js/vendor/pixi/"

tailwind:
	tailwindcss -i ./web/static/css/app.css -o ./web/static/css/dist/app.css --minify

tailwind-watch:
	tailwindcss -i ./web/static/css/app.css -o ./web/static/css/dist/app.css --watch

## ── Database ─────────────────────────────────────────────────────────────────

migrate-up:
	migrate -path db/migrations -database "$(DATABASE_URL)" up

migrate-down:
	migrate -path db/migrations -database "$(DATABASE_URL)" down 1

migrate-create:
	@test -n "$(NAME)" || (echo "Usage: make migrate-create NAME=<name>" && exit 1)
	migrate create -ext sql -dir db/migrations -seq $(NAME)

migrate-status:
	migrate -path db/migrations -database "$(DATABASE_URL)" version

sqlc-generate:
	$(SQLC) generate -f db/sqlc.yaml

sqlc-install:
	go install github.com/sqlc-dev/sqlc/cmd/sqlc@$(SQLC_VERSION)

# Fails when sqlc is missing or not $(SQLC_VERSION), when git fails, or when
# regenerating changes or adds any file under internal/db.
sqlc-check:
	@command -v $(SQLC) >/dev/null 2>&1 || { \
		echo "sqlc not found in $(SQLC_INSTALL_DIR) or on PATH: run 'make sqlc-install' (installs sqlc $(SQLC_VERSION))" >&2; \
		exit 1; \
	}
	@v=$$($(SQLC) version) || { echo "sqlc-check: 'sqlc version' failed" >&2; exit 1; }; \
	if [ "$$v" != "$(SQLC_VERSION)" ]; then \
		echo "sqlc $$v found, but internal/db is generated with $(SQLC_VERSION): run 'make sqlc-install'" >&2; \
		exit 1; \
	fi
	@$(MAKE) --no-print-directory sqlc-generate
	@out=$$(git status --porcelain -- internal/db) || { echo "sqlc-check: git status failed" >&2; exit 1; }; \
	if [ -n "$$out" ]; then \
		echo "internal/db is stale; run 'make sqlc-generate' and commit:" >&2; \
		echo "$$out"; \
		git diff -- internal/db; \
		exit 1; \
	fi

## ── Docker ───────────────────────────────────────────────────────────────────

docker-build:
	docker build -t $(APP_NAME):dev .

docker-run:
	docker run --rm -d \
	  -v "$(CURDIR)/.env":/config/.env:ro \
	  -p $(or $(PORT),8080):$(or $(PORT),8080) \
	  $(APP_NAME):dev

prod:
	docker build -t $(APP_NAME):prod --build-arg APP_ENV=production .

## ── Clean ────────────────────────────────────────────────────────────────────

clean:
	rm -rf $(BIN_DIR) tmp/
