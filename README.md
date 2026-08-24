# softsrv starter repo

This repo represents a generic golang web application. It started as an architecture.md file that I wrote and evolved based on suggestions from the LLM. Then that document was fed into claude and iterated on until it got to where I felt it was ready to be the base template repository for my other projects. I selected the different components of the tech stack with the goal of keeping everything fast and simple.

## Quickstart

Run the app locally for development with a single command:

```sh
make dev
```

`make dev` applies all pending database migrations (via [golang-migrate](https://github.com/golang-migrate/migrate)) against `$DATABASE_URL`, then starts the dev watchers in parallel: [air](https://github.com/air-verse/air) for Go hot-reload, a Tailwind CSS watch build, and the `smtp4dev` mail-catcher container. Re-running `make dev` is safe — `migrate ... up` is a no-op once the schema is already at head.

### Prerequisites

Before running `make dev`, make sure you have:

- **A reachable PostgreSQL database.** This project does not provision a local database for you — supply the connection string for an externally hosted/online Postgres instance (e.g. a managed cloud database) via `DATABASE_URL`. Migrations run against that database on every `make dev`.
- **A `.env` file.** Copy `.env.example` to `.env` and fill in the required values — the Makefile auto-loads `.env` (`-include .env`), and `.env` is gitignored so your local values stay local:

  ```sh
  cp .env.example .env
  ```

  The app fails fast at startup (exits non-zero) if any of these required variables is missing or empty: `APP_BASE_URL`, `DATABASE_URL`, `JWT_SECRET` (must be at least 32 bytes), `SMTP_HOST`, `SMTP_PORT`, `SMTP_FROM_EMAIL`.
- **Docker, installed and running.** `make dev` starts the `rnwood/smtp4dev` container for local email testing — SMTP on `localhost:2525`, web UI at [http://localhost:5000](http://localhost:5000).
- **Required CLI tooling on your `PATH`:**
  - [Go](https://go.dev/) (to build/run the app)
  - [`migrate`](https://github.com/golang-migrate/migrate) (golang-migrate CLI, used by `make migrate-up`)
  - [`air`](https://github.com/air-verse/air) (Go hot-reload)
  - [`tailwindcss`](https://tailwindcss.com/) (CSS watch/build)
