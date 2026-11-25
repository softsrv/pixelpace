# softsrv starter repo

This repo represents a generic golang web application. It started as an architecture.md file that I wrote and evolved based on suggestions from the LLM. Then that document was fed into claude and iterated on until it got to where I felt it was ready to be the base template repository for my other projects. I selected the different components of the tech stack with the goal of keeping everything fast and simple.

## Quickstart

Run the app locally for development with a single command:

```sh
make dev
```

`make dev` runs `docker compose up -d --build` on [compose.yaml](compose.yaml), which brings up the whole development stack:

- **postgres** (`postgres:17`, data in the `pgdata` volume), published on `localhost:5432` (user, password and database all `pixelpace`)
- **migrate**, a one-shot [golang-migrate](https://github.com/golang-migrate/migrate) run that applies pending migrations and exits; the app waits for it
- **valkey**, for live race fan-out, on `localhost:6379`
- **smtp4dev**, the mail-catcher: SMTP on `localhost:2525`, web UI at [http://localhost:5000](http://localhost:5000)
- **app**, built from `Dockerfile.dev`, with the source tree mounted at `/src`. [air](https://github.com/air-verse/air) rebuilds the Tailwind CSS and the Go binary on every change to Go, template, CSS or JS files and restarts the app on [http://localhost:8080](http://localhost:8080).

Re-running `make dev` is safe: compose recreates only what changed, and `migrate up` is a no-op at head.

```sh
make dev-logs        # follow the app and build output (docker compose logs -f app)
docker compose down  # stop everything (also: make dev-stop); the database survives
docker compose down -v  # stop and wipe the database and Go caches
```

`.env` is mounted read-only into the app container at `/config/.env`, where the app reads it. Compose overrides `DATABASE_URL`, `VALKEY_ADDR`, `SMTP_HOST` and `SMTP_PORT` so the app always uses the stack's own services, whatever `.env` points at. `PORT`, `DEV_PG_PORT`, `VALKEY_PORT`, `SMTP4DEV_SMTP_PORT` and `SMTP4DEV_WEB_PORT` change the host ports.

### Configuration file

The app loads its settings from a dotenv file at startup (via [godotenv](https://github.com/joho/godotenv)):

- `CONFIG_FILE` names the file. When it is set, the file must exist, or the app exits at startup.
- With `CONFIG_FILE` unset or empty, the app reads `./.env` if there is one, so `make run` and `go run ./cmd/app` pick up the repo's `.env`.
- Variables already set in the environment win over the file, so a single value can still be overridden at launch.
- The production image sets `CONFIG_FILE=/config/.env` and expects the file mounted there: `docker run -v "$PWD/.env":/config/.env:ro …` (`make docker-run` does this). The image runs as uid 65532, so on Linux the file must be readable by that user. Set `CONFIG_FILE` to another path to move it, or to an empty string to configure the container with environment variables alone.

### Prerequisites

Before running `make dev`, make sure you have:

- **A `.env` file.** Copy `.env.example` to `.env` and fill in the required values. The app reads it itself (see [Configuration file](#configuration-file)), the Makefile also loads it (`-include .env`) for targets such as `make migrate-up` (point its `DATABASE_URL` at the dev stack's Postgres, as `.env.example` does, so host tools use the same database), and `.env` is gitignored so your local values stay local:

  ```sh
  cp .env.example .env
  ```

  The app fails fast at startup (exits non-zero) if any of these required variables is missing or empty: `APP_BASE_URL`, `DATABASE_URL`, `JWT_SECRET` (must be at least 32 bytes), `SMTP_HOST`, `SMTP_PORT`, `SMTP_FROM_EMAIL`. Note that `JWT_SECRET` also derives the encryption key for Concept2 OAuth tokens at rest; rotating it forces every Concept2 connection to reconnect.

  Optional variables include `CONCEPT2_CLIENT_ID`, `CONCEPT2_CLIENT_SECRET`, and `CONCEPT2_BASE_URL` (defaults to `https://log-dev.concept2.com` until trusted client approval from Concept2; switch to `https://log.concept2.com` after approval). When configuring Concept2 integration, register `<APP_BASE_URL>/profile/concept2/callback` as the OAuth redirect URI with Concept2.
- **Docker with Compose v2, installed and running.** `make dev` runs the app and every service it needs in containers.
- **Required CLI tooling on your `PATH`:**
  - [`migrate`](https://github.com/golang-migrate/migrate) (golang-migrate CLI) only for `make migrate-*` from the host; `make dev` applies migrations itself
  - [Go](https://go.dev/) and [`tailwindcss`](https://tailwindcss.com/) only for running tests or `make run`/`make tailwind` outside Docker; `make dev` uses the copies in the dev image

## Running with Docker Compose

[deploy/docker-compose.yml](deploy/docker-compose.yml) runs the published `trriplejay/pixelpace` image with a private Valkey for live race updates. Postgres is external. (The repo-root `compose.yaml` is the development stack.) Run the commands below from the repo root; the file's relative paths resolve against `deploy/`, so it still reads the repo-root `.env` and `db/migrations`.

1. Create `.env` from `.env.example`. Use your hosted `DATABASE_URL`, `APP_ENV=production` and the public HTTPS origin as `APP_BASE_URL`. Set `VALKEY_ADDR=valkey:6379` to use the Valkey that compose starts. Compose mounts the file read-only at `/config/.env`, where the app reads it; on Linux, make it readable by uid 65532, the user the app runs as. To use a file other than the repo-root `.env`, set `PIXELPACE_CONFIG=/path/to/file`; the `migrate` service reads `DATABASE_URL` from the same file.
2. Apply migrations (they never run on startup):

   ```sh
   docker compose -f deploy/docker-compose.yml run --rm migrate
   ```

3. Start the app and Valkey:

   ```sh
   docker compose -f deploy/docker-compose.yml up -d
   ```

The app listens on `127.0.0.1:8080`, for a reverse proxy on the same host that serves HTTPS. Web Bluetooth and the production login cookies both require HTTPS. `APP_PORT` changes the port; `APP_BIND=0.0.0.0` exposes it on all interfaces. `PIXELPACE_TAG` pins an image tag such as `sha-6a7d038`, and `docker compose -f deploy/docker-compose.yml build` builds the image from this checkout instead. Set these in your shell: compose looks for a `.env` of its own in `deploy/`, not the repo root.

To update: `docker compose -f deploy/docker-compose.yml pull && docker compose -f deploy/docker-compose.yml run --rm migrate && docker compose -f deploy/docker-compose.yml up -d`.
