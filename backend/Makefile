.PHONY: test build differential wasm bundle test-bundle test-sqlite-profile test-projection-source-free native-icu-fetch native-icu-builder-test native-icu-build native-icu-test native-icu-source-free

# Native ICU is opt-in: default build/startup and the TypeScript image are unchanged.
ICU_WORK_DIR ?= $(CURDIR)/.native-build/icu
ICU_ARCHIVE ?= $(ICU_WORK_DIR)/node-v24.22.1.tar.xz
ICU_PROOF_ROOT ?= $(ICU_WORK_DIR)/source-free
PYTHON ?= python3

test:
	go test -race -tags sqlite_fts5 ./...
	go vet -tags sqlite_fts5 ./...

build:
	go build -tags sqlite_fts5 -o bin/humanengine ./cmd/humanengine

differential: build
	cd .. && HUMANENGINE_SOURCE_ROOT="$$PWD" node --import tsx backend/internal/installation/testdata/oracle.mts --check
	cd .. && HUMANENGINE_SOURCE_ROOT="$$PWD" node --import tsx backend/internal/installation/testdata/usage-oracle.mts --check
	cd .. && TZ=UTC node --import tsx backend/tests/generate-manual-fixtures.mjs --check
	cd .. && TZ=UTC node --import tsx backend/tests/generate-privacy-fixtures.mjs --check
	cd .. && TZ=UTC node --import tsx backend/internal/identityprivacy/testdata/generate-source-oracle.mjs --check
	cd .. && TZ=UTC node --import tsx backend/tests/generate-outer-proposal-fixtures.mjs --check
	cd .. && TZ=UTC node --import tsx backend/tests/generate-commitment-command-fixtures.mjs --check
	cd .. && TZ=UTC node --import tsx backend/internal/knowledge/testdata/generate-admission-oracle.ts --check
	cd .. && TZ=UTC node --import tsx backend/tests/generate-assertion-proposal-fixtures.mjs --check
	cd .. && TZ=UTC node --import tsx backend/tests/generate-reconciliation-fixtures.mjs --check
	cd .. && TZ=UTC node --import tsx backend/tests/generate-source-admission-fixtures.mjs --check
	cd .. && TZ=UTC node --import tsx backend/tests/generate-memory-service-fixtures.mjs --check
	cd .. && TZ=UTC node --import tsx backend/internal/knowledge/testdata/generate-read-oracle.ts --check
	cd .. && TZ=UTC node --import tsx backend/internal/memoryservice/testdata/generate-episode-oracle.ts --check
	cd .. && TZ=UTC node --import tsx backend/internal/knowledge/testdata/generate-source-oracle.ts --check
	cd .. && TZ=UTC node --import tsx backend/tests/generate-semantic-purge-fixtures.ts --check
	cd .. && TZ=UTC node --import tsx backend/tests/generate-workspace-resolve-fixtures.mjs --check
	cd .. && TZ=UTC node --import tsx backend/tests/generate-memory-list-fixtures.mjs --check
	cd .. && TZ=UTC node --import tsx backend/tests/generate-experience-view-fixtures.mjs --check
	cd .. && TZ=UTC node --import tsx backend/tests/generate-commitment-read-fixtures.mjs --check
	cd .. && TZ=UTC node --import tsx backend/tests/generate-working-set-fixtures.mjs --check
	cd .. && TZ=UTC node --import tsx backend/tests/generate-working-set-offline-fixtures.mjs --check
	cd .. && TZ=UTC node --import tsx backend/tests/generate-projection-fixtures.mjs --check
	cd .. && TZ=UTC node --import tsx backend/tests/generate-relation-fixtures.mjs --check
	cd .. && TZ=UTC node --import tsx backend/tests/generate-commitment-offline-fixtures.mjs --check
	cd .. && TZ=UTC node --import tsx backend/tests/generate-commitment-privacy-fixtures.mjs --check
	cd .. && TZ=UTC node --import tsx backend/tests/generate-proof-invalidation-fixtures.mjs --check
	cd .. && TZ=UTC node --import tsx backend/tests/generate-assertion-authority-fixtures.mjs --check
	cd .. && node --import tsx backend/tests/generate-support-read-fixtures.mjs --check
	cd .. && node backend/tests/generate-jsstring-fixtures.mjs --check
	cd .. && node --import tsx backend/tests/generate-assertion-history-fixtures.mjs --check
	cd .. && node --import tsx backend/tests/generate-assertion-coordinator-fixtures.mjs --check
	cd .. && node --import tsx backend/tests/generate-assertion-recovery-fixtures.mjs --check
	cd .. && node --import tsx backend/tests/generate-ingress-routing-fixtures.mjs --check
	cd .. && node --import tsx backend/tests/generate-runtime-source-authority-fixtures.mjs --check
	cd .. && node --import tsx backend/tests/generate-runtime-authority-fault-fixtures.mjs --check
	cd .. && node --import tsx backend/tests/generate-source-event-fixtures.mjs --check
	cd .. && node --import tsx backend/tests/generate-source-event-recovery-fixtures.mjs --check
	cd .. && node --import tsx backend/tests/generate-bot-runtime-fixtures.mjs --check
	cd .. && node backend/tests/generate-timezone-fixtures.mjs --check
	cd .. && node backend/internal/intltime/oracle.mjs --check
	cd .. && node --import tsx backend/tests/generate-reply-grounding-fixtures.mjs --check
	cd .. && node --import tsx backend/tests/generate-reply-handoff-fixtures.mjs --check
	cd .. && node --import tsx backend/internal/plugins/generate-v1-sdk.mjs --check
	cd .. && node --import tsx backend/tests/generate-prompt-cache-fixtures.mjs --check
	cd .. && node --import tsx backend/tests/generate-reply-plan-fixtures.mjs --check
	cd .. && node --import tsx backend/tests/generate-tool-result-fixtures.mjs --check
	cd .. && node backend/internal/jsdate/oracle.mjs --check
	cd .. && node backend/tests/generate-topic-collation-fixtures.mjs --check
	cd .. && node backend/tests/generate-topic-admission-fixtures.mjs --check
	cd .. && node --import tsx backend/tests/generate-topic-routing-state-fixtures.mjs --check
	cd .. && node --import tsx backend/tests/generate-topic-reassignment-fixtures.mjs --check
	cd .. && node --import tsx backend/tests/generate-secret-store-fixtures.mjs --check
	cd .. && node --import tsx backend/tests/generate-model-policy-fixtures.mjs --check
	cd .. && node --import tsx backend/tests/generate-http-string-fixtures.mjs --check
	cd .. && node --import tsx backend/tests/generate-credential-byte-fixtures.mjs --check
	cd .. && node --import tsx backend/tests/generate-authority-domain-fixtures.mjs --check
	cd .. && node --import tsx backend/tests/generate-settings-write-fixtures.mjs --check
	cd .. && node --import tsx backend/tests/generate-lossless-events-fixtures.mjs --check
	cd .. && node backend/tests/generate-sqlite-string-fixtures.mjs --check
	cd .. && node --import tsx backend/internal/recovery/testdata/generate-write-events-node.mjs --check
	cd .. && node --import tsx backend/internal/recovery/testdata/generate-remaining-write-events-node.mjs --check
	cd .. && node backend/tests/generate-jsjson-fixtures.mjs --check
	cd .. && node --import tsx backend/tests/generate-scene-write-fixtures.mjs --check
	cd .. && node --import tsx backend/tests/generate-dispatch-fixtures.mjs --check
	cd .. && node backend/tests/generate-v1-compat.mjs --check
	cd .. && node --import tsx backend/tests/generate-scheduler-fixtures.mjs --check
	cd .. && node --import tsx backend/tests/generate-turnwait-fixtures.mjs --check
	cd .. && node --import tsx backend/tests/config-differential.mjs backend/bin/humanengine

wasm:
	mkdir -p internal/plugins/builtins
	GOOS=wasip1 GOARCH=wasm go build -trimpath -buildmode=c-shared -ldflags='-s -w' -o internal/plugins/builtins/typing-delay.wasm ./plugins/typing-delay
	GOOS=wasip1 GOARCH=wasm go build -trimpath -buildmode=c-shared -ldflags='-s -w' -o internal/plugins/builtins/web-read.wasm ./plugins/web-read
	GOOS=wasip1 GOARCH=wasm go build -trimpath -buildmode=c-shared -ldflags='-s -w' -o internal/plugins/builtins/web-search.wasm ./plugins/web-search
	GOOS=wasip1 GOARCH=wasm go build -trimpath -buildmode=c-shared -ldflags='-s -w' -o internal/plugins/builtins/snowluma.wasm ./plugins/snowluma-wasm

bundle: wasm
	go build -trimpath -tags sqlite_fts5,bundled_wasm -o bin/humanengine ./cmd/humanengine
	cd .. && node backend/tests/bundled-source-free.mjs backend/bin/humanengine

test-bundle: wasm
	go test -tags sqlite_fts5,bundled_wasm ./internal/plugins -run TestDistributedArtifactsLoadWithoutSources

# Fetch and verify only when the configured archive is absent. The builder also
# verifies an existing archive; a mismatched artifact never silently updates.
$(ICU_ARCHIVE):
	$(PYTHON) native/icu/fetch.py --archive "$(ICU_ARCHIVE)"

native-icu-fetch:
	$(PYTHON) native/icu/fetch.py --archive "$(ICU_ARCHIVE)"

native-icu-builder-test:
	PYTHONDONTWRITEBYTECODE=1 $(PYTHON) -B native/icu/test_build.py -v
	PYTHONDONTWRITEBYTECODE=1 $(PYTHON) -B native/icu/test_fetch.py -v

native-icu-build: native-icu-builder-test $(ICU_ARCHIVE)
	$(PYTHON) native/icu/build.py --archive "$(ICU_ARCHIVE)" --work-dir "$(ICU_WORK_DIR)"

native-icu-test: native-icu-build
	. "$(ICU_WORK_DIR)/env.sh" && go test -race -tags sqlite_fts5,humanengine_icu ./...
	. "$(ICU_WORK_DIR)/env.sh" && go vet -tags sqlite_fts5,humanengine_icu ./...

native-icu-source-free: native-icu-build
	mkdir -p "$(ICU_PROOF_ROOT)"
	@out=$$(mktemp -d "$(ICU_PROOF_ROOT)/run.XXXXXXXX") && \
	  native/icu/test-source-free.sh "$(ICU_WORK_DIR)" "$$out" && \
	  echo "Native source-free proof: $$out"

# The backend must not start with SQLite projections silently unavailable.
test-sqlite-profile:
	cd .. && node backend/tests/sqlite-profile.mjs

# Real guarded SQLite FTS operations without Node, compiler or source directory.
test-projection-source-free:
	./tests/projection-source-free.sh

.PHONY: installation-bundle-check native-installation-check
installation-bundle-check:
	$(PYTHON) scripts/prepare-sqlite-vec.py --check
	go test -race -tags sqlite_fts5,bundled_sqlite_vec ./internal/vectorasset ./internal/installation
	sh tests/installation-source-free.sh

native-installation-check: native-icu-build
	. "$(ICU_WORK_DIR)/env.sh" && HUMANENGINE_TEST_REQUIRE_ICU=1 go test -race -tags sqlite_fts5,bundled_sqlite_vec,humanengine_icu ./internal/installation ./internal/vectorasset
	. "$(ICU_WORK_DIR)/env.sh" && HUMANENGINE_EXTRA_BUILD_TAGS=humanengine_icu sh tests/installation-source-free.sh
