GOOSE_VERSION := v3.28.0

# Release build identity, stamped into the binary via ldflags (-X main.Version,
# see cmd/api/version.go). Outside a git checkout (or when describe fails) this
# falls back to "dev", which is also what plain `go build` reports.
VERSION ?= $(shell git describe --tags --always 2>/dev/null || echo dev)

# Frontend sources/build output and the embed target it is copied into.
WEB_DIR := web
WEB_DIST := $(WEB_DIR)/dist
WEBUI_DIST := internal/webui/dist
RELEASE_BIN := bin/kanban-server
DATABASE_URL ?= postgres://kanban:kanban@127.0.0.1:54329/kanban?sslmode=disable
TEST_DATABASE_URL ?= postgres://kanban:kanban@127.0.0.1:54329/kanban_test?sslmode=disable
GOOSE := go run github.com/pressly/goose/v3/cmd/goose@$(GOOSE_VERSION)

.PHONY: dev-db dev-db-down generate migrate reset-test-db run test build web-build release test-release prune-events backup-db check-deployed

# Retention is opt-in manual maintenance: deletes events
# older than DAYS (default 30) from the DB named by KANBAN_DATABASE_URL
# (defaults to the live kanban DB). Override it for a one-off run, e.g. against
# the test DB: KANBAN_DATABASE_URL=…kanban_test make prune-events. Run
# backup-db first — this deletes rows.
DAYS ?= 30
prune-events:
	go run ./cmd/prune-events --days=$(DAYS)

# Standing convention: back up the live DB before any
# destructive DB op. The dump lands under ~/.local/share/kanban/backups/.
# pg_dump runs inside the dev container because the host often ships no
# postgres client; point DB_CONTAINER at the compose service's container to
# redirect the dump to another instance.
BACKUP_DIR ?= $(HOME)/.local/share/kanban/backups
DB_CONTAINER ?= api-first-kanban-db
backup-db:
	mkdir -p "$(BACKUP_DIR)"
	podman exec "$(DB_CONTAINER)" pg_dump -U kanban -d kanban > "$(BACKUP_DIR)/kanban-$$(date +%Y%m%d-%H%M%S).sql"

dev-db:
	podman compose up -d

dev-db-down:
	podman compose down

generate:
	go run github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1 generate

migrate:
	$(GOOSE) -dir migrations postgres "$(DATABASE_URL)" up

# Drop + recreate + migrate the DISPOSABLE test DB (kanban_test). Use when the
# test DB has accumulated rows from repeated `make test` / e2e runs (e.g. the
# board key-prefix space exhausting during go test). SAFETY: the DB name is
# hardcoded to kanban_test below AND the target parses the dbname out of
# TEST_DATABASE_URL (the path segment after the host, ignoring query params)
# and requires it to EQUAL kanban_test — a live-kanban DSN that merely contains
# the string "/kanban_test" somewhere (e.g. in a query parameter) is refused,
# so the live kanban DB cannot be touched by this target, by construction. DB
# ops run inside the dev container because the host often ships no postgres
# client.
reset-test-db:
	@dsn_db=$$(printf '%s' '$(TEST_DATABASE_URL)' | sed -E 's|^[a-z+]+://[^/]*/||; s|\?.*$$||'); \
	if [ "$$dsn_db" != "kanban_test" ]; then \
		echo "REFUSING: reset-test-db only operates on the kanban_test DB; TEST_DATABASE_URL's dbname is '$$dsn_db', not kanban_test ($(TEST_DATABASE_URL))" >&2; \
		exit 1; \
	fi
	podman exec "$(DB_CONTAINER)" psql -U kanban -d postgres -c "DROP DATABASE IF EXISTS kanban_test WITH (FORCE)" -c "CREATE DATABASE kanban_test OWNER kanban"
	$(GOOSE) -dir migrations postgres "$(TEST_DATABASE_URL)" up

run:
	go run ./cmd/api

test:
	KANBAN_TEST_DATABASE_URL="$(TEST_DATABASE_URL)" go test ./...

build:
	go build -o bin/api ./cmd/api

# Build the frontend bundle (web/dist) with vite. Needs pnpm/node; deliberately
# NOT a dependency of build/test, so a fresh clone without a frontend toolchain
# still compiles and tests against the committed placeholder dist (PB-93).
web-build:
	pnpm -C $(WEB_DIR) install --prefer-offline
	pnpm -C $(WEB_DIR) build

# Single-file release artifact: build the SPA, embed it, stamp the version.
# internal/webui/dist is wiped and repopulated from web/dist so a stale or
# dropped asset from an earlier build can never linger in the embed (PB-93
# review P2). The committed placeholder index.html is restored afterwards —
# go build has already read the real dist by then — so the working tree stays
# clean and the tree keeps no build output.
release: web-build
	@set -e; \
	placeholder=$$(mktemp); \
	trap 'rm -rf "$(WEBUI_DIST)"; mkdir -p "$(WEBUI_DIST)"; cp "$$placeholder" "$(WEBUI_DIST)/index.html"; rm -f "$$placeholder"' EXIT; \
	cp "$(WEBUI_DIST)/index.html" "$$placeholder"; \
	rm -rf "$(WEBUI_DIST)"; \
	mkdir -p "$(WEBUI_DIST)"; \
	cp -R "$(WEB_DIST)/." "$(WEBUI_DIST)/"; \
	leaked=$$(git status --porcelain --untracked-files=all -- "$(WEBUI_DIST)" | grep -v "$(WEBUI_DIST)/index.html" || true); \
	if [ -n "$$leaked" ]; then \
		echo "REFUSING: build output under $(WEBUI_DIST) is not git-ignored:" >&2; \
		echo "$$leaked" >&2; \
		exit 1; \
	fi; \
	go build -ldflags "-X main.Version=$(VERSION)" -o "$(RELEASE_BIN)" ./cmd/api; \
	echo "release: $(RELEASE_BIN) (version $(VERSION))"

# Gated release e2e (PB-97): build the real single-file artifact (pnpm
# web-build + fresh-dist embed), then run the black-box smoke suite that spawns
# it on an ephemeral loopback port against kanban_test. Deliberately NOT part of
# `test`: `make test` must stay green on a fresh clone with no node/pnpm (PB-93),
# and the vite build is the heavy step. Requires the dev DB stack up (dev-db).
test-release: release
	KANBAN_RELEASE_BIN="$(CURDIR)/$(RELEASE_BIN)" KANBAN_TEST_DATABASE_URL="$(TEST_DATABASE_URL)" \
		go test -count=1 -v ./test/releasee2e/

# Verify the RUNNING API was built from the current git HEAD — catches
# "merged but not deployed" drift (e.g. a commit that landed but the process
# was never restarted). Exits 1 when the server's build commit differs from
# HEAD; a "-dirty" server build is reported distinctly, not as stale.
API_URL ?= http://127.0.0.1:18080
check-deployed:
	@server=$$(curl -s "$(API_URL)/api/healthz" | sed -n 's/.*"commit":"\([^"]*\)".*/\1/p'); \
	head=$$(git rev-parse HEAD); \
	if [ "$$server" = "$$head" ]; then \
		echo "deployed: $$server matches HEAD"; \
	elif [ "$$server" = "$$head-dirty" ]; then \
		echo "deployed: $$server (HEAD plus uncommitted working-tree changes)"; \
	elif [ -z "$$server" ] || [ "$$server" = "unknown" ]; then \
		echo "UNVERIFIABLE: server reports no VCS revision (built outside git, or from a linked worktree, where the Go toolchain does not stamp VCS); rebuild from the main checkout or set KANBAN_COMMIT" >&2; \
		exit 1; \
	else \
		echo "STALE: server reports commit '$$server' but HEAD is '$$head' — restart the API (make build && make run)" >&2; \
		exit 1; \
	fi

# PB-142 (A5): repository hygiene certification. Non-zero exit on any
# MUST-FIX finding. See scripts/audit/README.md.
.PHONY: audit
audit:
	@bash scripts/audit/scan.sh .
