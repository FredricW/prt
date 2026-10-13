# prt — see CLAUDE.md for the contract of each target.
export CGO_ENABLED := 1

FIXTURE_SRC  := testdata/fixtures
FIXTURE_REPO := testdata/fixture-repo
FIXTURE_PRS  := testdata/fixtures/prs.json
# Fixed clock for snapshots so ages are byte-stable.
SNAPSHOT_NOW := 2026-10-13T12:00:00Z

.PHONY: build test lint check demo fixtures snapshot record-jev record-jev-dry record-embed

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

RECORDINGS := testdata/recordings
# Offline demo/snapshot replay the recorded Jev answers and embeddings; a
# replay miss degrades to "labels unavailable" / "no embeddings".
REPLAY := --jev-replay $(RECORDINGS)/jev --embed-replay $(RECORDINGS)/embed

# Interactive TUI against the fixture repo, fully offline.
demo: build $(FIXTURE_REPO)/.git
	./bin/prt --no-daemon --offline $(REPLAY) --repo $(FIXTURE_REPO) --fixtures $(FIXTURE_PRS)

# Record real Jev answers for the fixture PRs into testdata/recordings/jev
# (needs JEV_API_KEY; spend capped at $1.00, ledger in testdata/recordings/SPEND.md).
# Run once after parse/labels/pipeline are final; tests replay the recordings.
record-jev:
	go run ./tools/recordjev --live --embed replay

# Free dry run: counts the requests record-jev would send; no network.
record-jev-dry:
	go run ./tools/recordjev --embed replay

# Record Ollama embeddings for every fixture unit into testdata/recordings/embed
# (local and free; needs Ollama with embeddinggemma-2).
record-embed:
	go run ./tools/recordjev --embed live

# Non-interactive: render a few screens to stdout (for agents and CI).
snapshot: build $(FIXTURE_REPO)/.git
	./bin/prt --no-daemon --offline $(REPLAY) --snapshot --width 120 --height 30 --now $(SNAPSHOT_NOW) --repo $(FIXTURE_REPO) --fixtures $(FIXTURE_PRS)
