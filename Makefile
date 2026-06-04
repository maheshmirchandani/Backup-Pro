.PHONY: build build-faultinject test test-pkg test-faultinject e2e-fast e2e-safety bench bench-baseline coverage verify-release snapshot-update lint ci-local debug-bundle clean

SOURCE_DATE_EPOCH ?= $(shell git log -1 --format=%ct 2>/dev/null || echo 0)
export SOURCE_DATE_EPOCH

GOFLAGS := -trimpath -buildvcs=false

# LDFLAGS for release builds: strip DWARF (smaller binary), strip buildid for
# reproducibility, KEEP the Go symbol table so the verify-release gate
# (invariant #35) can scan it with `go tool nm`, and flip the
# codesign.IsReleaseBuild string var to "true" so the per-launch codesign
# self-verify gate runs against the on-disk binary (spec invariant #29 /
# Task 18). Dev builds (go run, go test, plain go build) leave
# IsReleaseBuild at its compile-time default of "false" and skip the gate.
#
# We intentionally do NOT pass -s (which would strip the Go symbol table).
# Task 28 review (2026-06-04) caught that -s -w made the nm-based symbol
# scan a no-op: nm returns ~58 libc symbols and nothing else, so any
# faultinject leak would be invisible. Keeping the symbol table costs ~1MB
# in binary size; we treat that as the cost of having a working release
# gate.
LDFLAGS_RELEASE := -w -buildid= -X github.com/maheshmirchandani/Backup-Pro/internal/preflight/codesign.IsReleaseBuild=true

# LDFLAGS for the faultinject build: matches release flag policy (strip
# DWARF, keep symbols) so the build artifact has the same symbol
# visibility as a release binary. IsReleaseBuild stays "false" so codesign
# isn't invoked against an unsigned faultinject artifact during e2e runs.
LDFLAGS_FAULTINJECT := -w -buildid=

build:
	@if [ -d ./cmd/flashbackup ]; then \
		go build $(GOFLAGS) -tags release -ldflags "$(LDFLAGS_RELEASE)" -o flashbackup ./cmd/flashbackup; \
	else \
		echo "skip: ./cmd/flashbackup does not exist yet"; \
	fi

build-faultinject:
	@if [ -d ./cmd/flashbackup ]; then \
		go build $(GOFLAGS) -tags faultinject -ldflags "$(LDFLAGS_FAULTINJECT)" -o flashbackup-faultinject ./cmd/flashbackup; \
	else \
		echo "skip: ./cmd/flashbackup does not exist yet"; \
	fi

test:
	go test -timeout=2m ./...

# Per-package test for fast TDD loop:  make test-pkg PKG=./internal/state
test-pkg:
	go test -timeout=2m $(PKG)

# Skip-if-missing helpers: during early development tasks 1-41 build internal/
# code; test/e2e/ doesn't exist until Task 42. Returning success when the dir
# is absent lets CI stay green while plan progresses. Once test/e2e/ exists,
# tests must run for real.
test-faultinject:
	@if [ -d ./test/e2e ]; then \
		go test -timeout=5m -tags faultinject ./test/e2e/...; \
	else \
		echo "skip: ./test/e2e/ does not exist yet"; \
	fi

# e2e split per PS2:
#  e2e-fast: gates PR merge; happy paths only
#  e2e-safety: gates main push; faultinject + hdiutil-heavy; flaky-tolerant on PR
e2e-fast:
	@if [ -d ./test/e2e ]; then \
		FLASHBACKUP_E2E=1 go test -timeout=5m -run "Init|BackupHappy|VerifyIntact|LockContention|NonTTY" ./test/e2e/...; \
	else \
		echo "skip: ./test/e2e/ does not exist yet"; \
	fi

e2e-safety:
	@if [ -d ./test/e2e ]; then \
		FLASHBACKUP_E2E=1 go test -timeout=15m -tags faultinject -run "AtomicGate|Mutation|CrashResume|DeleteFlag|DeleteConfirm|TamperedManifest" ./test/e2e/...; \
	else \
		echo "skip: ./test/e2e/ does not exist yet"; \
	fi

# Bench dirs: skip the ones that don't exist yet.
bench:
	@dirs=""; for d in ./internal/hash ./internal/state ./internal/runner; do \
		[ -d $$d ] && dirs="$$dirs $$d"; \
	done; \
	if [ -n "$$dirs" ]; then \
		go test -bench=. -benchmem -count=5 -timeout=10m $$dirs; \
	else \
		echo "skip: no bench dirs exist yet"; \
	fi

bench-baseline:
	@dirs=""; for d in ./internal/hash ./internal/state ./internal/runner; do \
		[ -d $$d ] && dirs="$$dirs $$d"; \
	done; \
	if [ -n "$$dirs" ]; then \
		go test -bench=. -benchmem -count=5 $$dirs | tee testdata/benchmarks-baseline.txt; \
	else \
		echo "skip: no bench dirs exist yet"; \
	fi

# Per-package coverage gates: runner/state/hash/preflight >=80% statement-line per invariant #42.
# Runs `go test -cover` per package and parses the "coverage: X% of statements" line; this is the
# real statement-weighted coverage Go reports (not a per-function average, which would inflate the
# number on small util functions). Each safety-critical package is independently gated; packages
# that don't exist yet are skipped (the gate kicks in per package as it lands).
#
# FLASHBACKUP_E2E=1 is set per-package because the runner package's Run state machine cannot be
# meaningfully tested without exercising the full preflight + rsync pipeline (which requires
# hdiutil + a GNU rsync). On hosts where hdiutil or GNU rsync is missing, the e2e tests skip and
# the coverage gate may dip below 80%; install via `brew install rsync` and retry on macOS.
coverage:
	@failed=""; ran=""; tmpdir=$$(mktemp -d); \
	for pkg in runner hash state preflight; do \
		[ -d ./internal/$$pkg ] || continue; \
		ran="$$ran $$pkg"; \
		profile=$$tmpdir/$$pkg.cover; \
		FLASHBACKUP_E2E=1 go test -timeout=5m -cover -coverpkg=./internal/$$pkg/... -coverprofile=$$profile ./internal/$$pkg/... >/dev/null 2>&1 || true; \
		if [ ! -s "$$profile" ] || [ $$(wc -l < "$$profile") -le 1 ]; then \
			printf "%-12s -- (no statements; vacuously covered)\n" "$$pkg"; \
			continue; \
		fi; \
		pct=$$(go tool cover -func=$$profile 2>/dev/null | awk '/^total:/{gsub(/%/,"",$$3); print $$3}'); \
		if [ -z "$$pct" ]; then \
			printf "%-12s -- (cover tool returned no total)\n" "$$pkg"; \
			continue; \
		fi; \
		printf "%-12s %s%% (tree-weighted)\n" "$$pkg" "$$pct"; \
		awk -v p=$$pct 'BEGIN{if(p+0 < 80){exit 1}}' || failed="$$failed $$pkg"; \
	done; \
	rm -rf "$$tmpdir"; \
	if [ -z "$$ran" ]; then echo "skip: no safety-critical packages exist yet"; exit 0; fi; \
	if [ -n "$$failed" ]; then echo "FAIL: below 80%:$$failed"; exit 1; fi; \
	echo "OK: all safety-critical packages >= 80%"

# Release-gate symbol-scan per invariant #35: assert no faultinject hooks leak into release build
# Anchor regex to avoid false positives on legitimate symbols containing "Inject".
verify-release: build
	@if [ ! -f flashbackup ]; then \
		echo "skip: flashbackup binary not built (cmd/flashbackup absent)"; \
	elif go tool nm flashbackup | grep -E '(^|[._/])faultinject' >/dev/null 2>&1; then \
		echo "FAIL: faultinject symbols found in release binary"; exit 1; \
	else \
		echo "OK: release binary clean of faultinject symbols"; \
	fi

# Debug bundle for friend bug reports: make debug-bundle RUN=2026-06-03T1430Z-a7f2 USB=/Volumes/FLASHBKP
debug-bundle:
	@test -n "$(RUN)" || (echo "usage: make debug-bundle RUN=<run-id> USB=<usb-path>"; exit 1)
	@test -n "$(USB)" || (echo "usage: make debug-bundle RUN=<run-id> USB=<usb-path>"; exit 1)
	tar -czvf flashbackup-debug-$(RUN).tgz \
		-C $(USB)/.flashbackup runs/$(RUN) version.json \
		-C $(USB)/.flashbackup runs.ndjson
	@echo "wrote flashbackup-debug-$(RUN).tgz"

lint:
	@unfmt=$$(gofmt -s -l . 2>/dev/null); \
		if [ -n "$$unfmt" ]; then \
			echo "FAIL: unformatted files (run 'gofmt -s -w .'):"; \
			echo "$$unfmt"; \
			exit 1; \
		fi
	go vet ./...
	golangci-lint run

ci-local: lint test test-faultinject e2e-fast e2e-safety verify-release coverage

clean:
	rm -f flashbackup flashbackup-faultinject coverage.out
	rm -rf dist/
