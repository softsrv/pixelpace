# softsrv starter repo

This repo represents a generic golang web application. It started as an architecture.md file that I wrote and evolved based on suggestions from the LLM. Then that document was fed into claude and iterated on until it got to where I felt it was ready to be the base template repository for my other projects. I selected the different components of the tech stack with the goal of keeping everything fast and simple.

## Quickstart

Run the app locally for development with a single command:

```sh
make dev
```

`make dev` starts the `smtp4dev` mail-catcher and Valkey containers, applies all pending database migrations (via [golang-migrate](https://github.com/golang-migrate/migrate)) against `$DATABASE_URL`, then runs the app in a detached Docker container (`app-dev`, built from `Dockerfile.dev`) and prints its container ID. The source tree is mounted into the container, where [air](https://github.com/air-verse/air) rebuilds the Tailwind CSS and the Go binary on every change to Go, template, CSS or JS files and restarts the app on [http://localhost:8080](http://localhost:8080). Re-running `make dev` is safe: it replaces the running container, and `migrate ... up` is a no-op once the schema is already at head.

```sh
make dev-logs   # follow the app and build output
make dev-stop   # stop and remove the app container
```

Inside the container, `localhost` in `DATABASE_URL`, `VALKEY_ADDR` and `SMTP_HOST` is rewritten to `host.docker.internal`, so services running on your machine stay reachable.

### Prerequisites

Before running `make dev`, make sure you have:

- **A reachable PostgreSQL database.** This project does not provision a local database for you — supply the connection string for an externally hosted/online Postgres instance (e.g. a managed cloud database) via `DATABASE_URL`. Migrations run against that database on every `make dev`.
- **A `.env` file.** Copy `.env.example` to `.env` and fill in the required values — the Makefile auto-loads `.env` (`-include .env`), and `.env` is gitignored so your local values stay local:

  ```sh
  cp .env.example .env
  ```

  The app fails fast at startup (exits non-zero) if any of these required variables is missing or empty: `APP_BASE_URL`, `DATABASE_URL`, `JWT_SECRET` (must be at least 32 bytes), `SMTP_HOST`, `SMTP_PORT`, `SMTP_FROM_EMAIL`. Note that `JWT_SECRET` also derives the encryption key for Concept2 OAuth tokens at rest; rotating it forces every Concept2 connection to reconnect.

  Optional variables include `CONCEPT2_CLIENT_ID`, `CONCEPT2_CLIENT_SECRET`, and `CONCEPT2_BASE_URL` (defaults to `https://log-dev.concept2.com` until trusted client approval from Concept2; switch to `https://log.concept2.com` after approval). When configuring Concept2 integration, register `<APP_BASE_URL>/profile/concept2/callback` as the OAuth redirect URI with Concept2.
- **Docker, installed and running.** `make dev` runs the app itself in a container, and starts the `rnwood/smtp4dev` container for local email testing — SMTP on `localhost:2525`, web UI at [http://localhost:5000](http://localhost:5000).
- **Required CLI tooling on your `PATH`:**
  - [`migrate`](https://github.com/golang-migrate/migrate) (golang-migrate CLI, used by `make migrate-up`)
  - [Go](https://go.dev/) and [`tailwindcss`](https://tailwindcss.com/) only for running tests or `make run`/`make tailwind` outside Docker; `make dev` uses the copies in the dev image

## Running with Docker Compose

`docker-compose.yml` runs the published `trriplejay/pixelpace` image with a private Valkey for live race updates. Postgres stays external, as in development.

1. Create `.env` from `.env.example`. Use your hosted `DATABASE_URL`, `APP_ENV=production` and the public HTTPS origin as `APP_BASE_URL`. Set `VALKEY_ADDR=valkey:6379` to use the Valkey that compose starts.
2. Apply migrations (they never run on startup):

   ```sh
   docker compose run --rm migrate
   ```

3. Start the app and Valkey:

   ```sh
   docker compose up -d
   ```

The app listens on `127.0.0.1:8080`, for a reverse proxy on the same host that serves HTTPS. Web Bluetooth and the production login cookies both require HTTPS. `APP_PORT` changes the port; `APP_BIND=0.0.0.0` exposes it on all interfaces. `PIXELPACE_TAG` pins an image tag such as `sha-6a7d038`, and `docker compose build` builds the image from this checkout instead.

To update: `docker compose pull && docker compose run --rm migrate && docker compose up -d`.
