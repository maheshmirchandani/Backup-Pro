.PHONY: build build-faultinject test test-pkg test-faultinject e2e-fast e2e-safety bench bench-baseline coverage verify-release snapshot-update lint ci-local debug-bundle clean

SOURCE_DATE_EPOCH ?= $(shell git log -1 --format=%ct 2>/dev/null || echo 0)
export SOURCE_DATE_EPOCH

GOFLAGS := -trimpath -buildvcs=false -ldflags "-s -w -buildid="

build:
	@if [ -d ./cmd/flashbackup ]; then \
		go build $(GOFLAGS) -tags release -o flashbackup ./cmd/flashbackup; \
	else \
		echo "skip: ./cmd/flashbackup does not exist yet"; \
	fi

build-faultinject:
	@if [ -d ./cmd/flashbackup ]; then \
		go build $(GOFLAGS) -tags faultinject -o flashbackup-faultinject ./cmd/flashbackup; \
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

# Per-package coverage gates: runner/state/hash/preflight >=80% line per invariant #42
# Use trailing slash anchor to avoid substring matches (e.g., internal/state vs internal/statemachine).
# Each safety-critical package is independently gated. Packages that don't exist yet
# are skipped (the gate kicks in once the package lands).
coverage:
	@dirs=""; for pkg in runner hash state preflight; do \
		[ -d ./internal/$$pkg ] && dirs="$$dirs ./internal/$$pkg/..."; \
	done; \
	if [ -z "$$dirs" ]; then echo "skip: no safety-critical packages exist yet"; exit 0; fi; \
	go test -coverprofile=coverage.out -covermode=atomic $$dirs
	@for pkg in runner hash state preflight; do \
		[ -d ./internal/$$pkg ] || continue; \
		pct=$$(go tool cover -func=coverage.out | grep -E "internal/$$pkg(/|\.go:)" | awk '{sum+=$$3+0;n+=1} END {if(n>0)printf "%.1f",sum/n;else print "0"}'); \
		echo "$$pkg coverage: $$pct%"; \
		awk -v p=$$pct 'BEGIN{if(p+0 < 80){exit 1}}' || { echo "FAIL: $$pkg below 80%"; exit 1; }; \
	done

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
