.PHONY: build build-faultinject test test-pkg test-faultinject e2e-fast e2e-safety bench bench-baseline coverage verify-release snapshot-update lint ci-local debug-bundle clean

SOURCE_DATE_EPOCH ?= $(shell git log -1 --format=%ct 2>/dev/null || echo 0)
export SOURCE_DATE_EPOCH

GOFLAGS := -trimpath -buildvcs=false -ldflags "-s -w -buildid="

build:
	go build $(GOFLAGS) -tags release -o flashbackup ./cmd/flashbackup

build-faultinject:
	go build $(GOFLAGS) -tags faultinject -o flashbackup-faultinject ./cmd/flashbackup

test:
	go test -timeout=2m ./internal/... ./cmd/...

# Per-package test for fast TDD loop:  make test-pkg PKG=./internal/state
test-pkg:
	go test -timeout=2m $(PKG)

test-faultinject:
	go test -timeout=5m -tags faultinject ./test/e2e/...

# e2e split per PS2:
#  e2e-fast: gates PR merge; happy paths only
#  e2e-safety: gates main push; faultinject + hdiutil-heavy; flaky-tolerant on PR
e2e-fast:
	FLASHBACKUP_E2E=1 go test -timeout=5m -run "Init|BackupHappy|VerifyIntact|LockContention|NonTTY" ./test/e2e/...

e2e-safety:
	FLASHBACKUP_E2E=1 go test -timeout=15m -tags faultinject -run "AtomicGate|Mutation|CrashResume|DeleteFlag|DeleteConfirm|TamperedManifest" ./test/e2e/...

bench:
	go test -bench=. -benchmem -count=5 -timeout=10m ./internal/hash ./internal/state ./internal/runner

bench-baseline:
	go test -bench=. -benchmem -count=5 ./internal/hash ./internal/state ./internal/runner | tee testdata/benchmarks-baseline.txt

# Per-package coverage gates: runner/state/hash/preflight >=80% line per invariant #42
coverage:
	go test -coverprofile=coverage.out -covermode=atomic ./internal/runner/... ./internal/hash/... ./internal/state/... ./internal/preflight/...
	@for pkg in runner hash state preflight; do \
		pct=$$(go tool cover -func=coverage.out | grep "internal/$$pkg" | awk '{sum+=$$3+0;n+=1} END {if(n>0)printf "%.1f",sum/n;else print "0"}'); \
		echo "$$pkg coverage: $$pct%"; \
		awk -v p=$$pct 'BEGIN{if(p+0 < 80){exit 1}}' || { echo "FAIL: $$pkg below 80%"; exit 1; }; \
	done

# Release-gate symbol-scan per invariant #35: assert no faultinject hooks leak into release build
verify-release: build
	@if go tool nm flashbackup | grep -Ei 'faultinject|Inject' >/dev/null 2>&1; then \
		echo "FAIL: faultinject symbols found in release binary"; exit 1; \
	fi
	@echo "OK: release binary clean of faultinject symbols"

# Debug bundle for friend bug reports: make debug-bundle RUN=2026-06-03T1430Z-a7f2 USB=/Volumes/FLASHBKP
debug-bundle:
	@test -n "$(RUN)" || (echo "usage: make debug-bundle RUN=<run-id> USB=<usb-path>"; exit 1)
	@test -n "$(USB)" || (echo "usage: make debug-bundle RUN=<run-id> USB=<usb-path>"; exit 1)
	tar -czvf flashbackup-debug-$(RUN).tgz \
		-C $(USB)/.flashbackup runs/$(RUN) version.json \
		-C $(USB)/.flashbackup runs.ndjson
	@echo "wrote flashbackup-debug-$(RUN).tgz"

lint:
	gofmt -s -d .
	go vet ./...
	golangci-lint run

ci-local: lint test test-faultinject e2e-fast e2e-safety verify-release coverage

clean:
	rm -f flashbackup flashbackup-faultinject coverage.out
	rm -rf dist/
