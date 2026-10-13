# prt — see CLAUDE.md for the contract of each target.
export CGO_ENABLED := 1

FIXTURE_SRC  := testdata/fixtures
FIXTURE_REPO := testdata/fixture-repo
FIXTURE_PRS  := testdata/fixtures/prs.json
# Fixed clock for snapshots so ages are byte-stable.
SNAPSHOT_NOW := 2026-10-13T12:00:00Z

.PHONY: build test lint check demo fixtures snapshot

build:
	CGO_ENABLED=1 go build -o bin/prt ./cmd/prt

test:
	CGO_ENABLED=1 go test ./...

lint:
	golangci-lint run ./...

# Must pass before every commit. The live-tagged vet compiles the
# `//go:build live` tests without running them, so they cannot rot.
check:
	go vet ./...
	go vet -tags live ./...
	$(MAKE) test
	$(MAKE) lint

# Every committed fixture source; the generated repo is rebuilt when any changes.
FIXTURE_DEPS := $(shell find $(FIXTURE_SRC) -type f) internal/fixturegen/fixturegen.go

# Regenerate the fixture repo from committed sources (deterministic SHAs).
fixtures:
	go run ./tools/fixturegen -src $(FIXTURE_SRC) -out $(FIXTURE_REPO)

$(FIXTURE_REPO)/.git: $(FIXTURE_DEPS)
	go run ./tools/fixturegen -src $(FIXTURE_SRC) -out $(FIXTURE_REPO)
	touch $@

# Interactive TUI against the fixture repo, fully offline.
demo: build $(FIXTURE_REPO)/.git
	./bin/prt --no-daemon --offline --repo $(FIXTURE_REPO) --fixtures $(FIXTURE_PRS)

# Non-interactive: render a few screens to stdout (for agents and CI).
snapshot: build $(FIXTURE_REPO)/.git
	./bin/prt --no-daemon --offline --snapshot --width 120 --height 30 --now $(SNAPSHOT_NOW) --repo $(FIXTURE_REPO) --fixtures $(FIXTURE_PRS)
