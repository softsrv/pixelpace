APP_NAME         := app
BIN_DIR          := ./bin
MODULE           := github.com/softsrv/starter
SMTP4DEV_NAME    := $(APP_NAME)-smtp4dev
SMTP4DEV_SMTP    := 2525
SMTP4DEV_WEB     := 5000

# Load .env if present (for local dev convenience)
-include .env
export

.PHONY: dev run build test fmt lint \
        daisyui-install tailwind tailwind-watch \
        smtp4dev smtp4dev-stop \
        migrate-up migrate-down migrate-create migrate-status \
        sqlc-generate \
        docker-build docker-run prod clean

## ── Development ─────────────────────────────────────────────────────────────

# Full hot-reload: smtp4dev container + Go (air) + Tailwind watch in parallel
dev: smtp4dev
	$(MAKE) -j2 air tailwind-watch

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
# can resolve them. Re-run this whenever you upgrade DaisyUI.
daisyui-install:
	curl -sLo web/static/css/daisyui.mjs \
	  https://github.com/saadeghi/daisyui/releases/latest/download/daisyui.mjs
	curl -sLo web/static/css/daisyui-theme.mjs \
	  https://github.com/saadeghi/daisyui/releases/latest/download/daisyui-theme.mjs
	@echo "DaisyUI bundles downloaded to web/static/css/"

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
	sqlc generate -f db/sqlc.yaml

## ── Docker ───────────────────────────────────────────────────────────────────

docker-build:
	docker build -t $(APP_NAME):dev .

docker-run:
	docker run --rm -d \
	  --env-file .env \
	  -p $(or $(PORT),8080):$(or $(PORT),8080) \
	  $(APP_NAME):dev

prod:
	docker build -t $(APP_NAME):prod --build-arg APP_ENV=production .

## ── Clean ────────────────────────────────────────────────────────────────────

clean:
	rm -rf $(BIN_DIR) tmp/
