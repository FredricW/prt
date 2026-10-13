# prt — see CLAUDE.md for the contract of each target.
export CGO_ENABLED := 1

FIXTURE_SRC  := testdata/fixtures
FIXTURE_REPO := testdata/fixture-repo
FIXTURE_PRS  := testdata/fixtures/prs.json
# Local draft comments the demo and snapshot start with (kept once stored).
FIXTURE_DRAFTS := testdata/fixtures/drafts.json
# Fixed clock for snapshots so ages are byte-stable.
SNAPSHOT_NOW := 2026-10-13T12:00:00Z
# M5.3 fixture workspace: portal (the fixture repo again) and billing-svc,
# plus a plain directory, decoy repos and a symlink loop discovery skips.
FIXTURE_WS_SRC      := testdata/fixtures-workspace
FIXTURE_WS          := testdata/fixture-workspace
FIXTURE_WS_MANIFEST := $(FIXTURE_WS_SRC)/manifest.json

.PHONY: build test lint check demo demo-workspace fixtures snapshot snapshot-workspace quality record-jev record-jev-dry record-review record-review-dry record-embed

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
# The workspace also embeds the fixture repo (portal), so it depends on both.
FIXTURE_WS_DEPS := $(shell find $(FIXTURE_WS_SRC) -type f) $(FIXTURE_DEPS) internal/fixturegen/workspace.go

# Regenerate the fixture repo and the fixture workspace from committed
# sources (deterministic SHAs).
fixtures:
	go run ./tools/fixturegen -src $(FIXTURE_SRC) -out $(FIXTURE_REPO)
	go run ./tools/fixturegen -workspace $(FIXTURE_WS_MANIFEST) -out $(FIXTURE_WS)

$(FIXTURE_REPO)/.git: $(FIXTURE_DEPS)
	go run ./tools/fixturegen -src $(FIXTURE_SRC) -out $(FIXTURE_REPO)
	touch $@

# Stamped by its portal repo's .git (the workspace is rebuilt as a whole).
$(FIXTURE_WS)/portal/.git: $(FIXTURE_WS_DEPS)
	go run ./tools/fixturegen -workspace $(FIXTURE_WS_MANIFEST) -out $(FIXTURE_WS)
	touch $@

RECORDINGS := testdata/recordings
# Offline demo/snapshot replay the recorded Jev answers, embeddings and
# reviews; a replay miss degrades to "labels unavailable" / "no embeddings" /
# an unreviewed group.
REPLAY := --jev-replay $(RECORDINGS)/jev --embed-replay $(RECORDINGS)/embed --review-replay $(RECORDINGS)/review

# Interactive TUI against the fixture repo, fully offline. The clock is
# pinned like the snapshot's: the fixture branch's recorded branch-state
# request carries its idle days, which the wall clock would move.
demo: build $(FIXTURE_REPO)/.git
	./bin/prt --no-daemon --offline $(REPLAY) --now $(SNAPSHOT_NOW) --repo $(FIXTURE_REPO) --fixtures $(FIXTURE_PRS) --drafts $(FIXTURE_DRAFTS)

# Interactive TUI over the fixture workspace (M5.3), fully offline: both
# repos in one picker. Only portal has recordings; billing-svc's replay
# misses show "labels unavailable" and unreviewed groups. --root is explicit
# because the workspace sits inside prt's own work tree.
demo-workspace: build $(FIXTURE_WS)/portal/.git
	./bin/prt --root $(FIXTURE_WS) --depth 2 --no-daemon --offline $(REPLAY) --now $(SNAPSHOT_NOW) --fixtures $(FIXTURE_WS_MANIFEST)

# Record real Jev answers for the fixture PRs into testdata/recordings/jev
# (needs JEV_API_KEY; spend capped at $1.00, ledger in testdata/recordings/SPEND.md).
# Run once after parse/labels/pipeline are final; tests replay the recordings.
record-jev:
	go run ./tools/recordjev --live --embed replay --branches

# Free dry run: counts the requests record-jev would send; no network.
record-jev-dry:
	go run ./tools/recordjev --embed replay --branches

# Record real Anthropic reviews, TL;DRs and group titles for the fixture PRs
# into testdata/recordings/review (needs PRT_ANTHROPIC_API_KEY; spend capped
# at $3.00 for anthropic, $5.00 project-wide; ledger in SPEND.md). Jev answers
# and embeddings come from the existing recordings.
record-review:
	go run ./tools/recordjev --live --review --embed replay --branches

# Free dry run: counts the review requests and their upper-bound cost; no network.
record-review-dry:
	go run ./tools/recordjev --review --embed replay --branches

# Record Ollama embeddings for every fixture unit into testdata/recordings/embed
# (local and free; needs Ollama with embeddinggemma-2).
record-embed:
	go run ./tools/recordjev --embed live

# Label-quality scoreboard (M5.2): runs the pipeline offline over the fixture
# items with the recorded answers and scores it against testdata/groundtruth/
# (method and baseline in docs/QUALITY.md). Flags: -json, -explain.
quality:
	CGO_ENABLED=1 go run ./tools/quality

# Non-interactive: render a few screens to stdout (for agents and CI).
# Six items so the fixture branch (05-spike-dark-mode) shows its branch-state
# block and #106 (Involved section, ranked last) renders too.
snapshot: build $(FIXTURE_REPO)/.git
	./bin/prt --no-daemon --offline $(REPLAY) --snapshot --width 120 --height 30 --max-items 6 --now $(SNAPSHOT_NOW) --repo $(FIXTURE_REPO) --fixtures $(FIXTURE_PRS) --drafts $(FIXTURE_DRAFTS)

# The workspace snapshot (M5.3): the repo-grouped picker, up to three items
# per repo, then the folded and filtered workspace screens.
snapshot-workspace: build $(FIXTURE_WS)/portal/.git
	./bin/prt --root $(FIXTURE_WS) --depth 2 --no-daemon --offline $(REPLAY) --snapshot --width 120 --height 30 --max-items 3 --now $(SNAPSHOT_NOW) --fixtures $(FIXTURE_WS_MANIFEST)
