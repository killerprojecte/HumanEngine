.PHONY: builtin-source-check plugin-source-check plugin-layout-check unbundled-plugin-graph-check external-only-plugin-graph-check clean-wasm clean contextual-source-check
.PHONY: test build differential wasm bundle test-bundle test-sqlite-profile test-projection-source-free native-icu-fetch native-icu-builder-test native-icu-build native-icu-test native-icu-source-free

# Native ICU is opt-in: default build/startup and the TypeScript image are unchanged.
ICU_WORK_DIR ?= $(CURDIR)/.native-build/icu
ICU_ARCHIVE ?= $(ICU_WORK_DIR)/node-v24.22.1.tar.xz
ICU_PROOF_ROOT ?= $(ICU_WORK_DIR)/source-free
PYTHON ?= python3

test: test-runtime-preview-profile
	go test -race -tags sqlite_fts5 ./...
	go vet -tags sqlite_fts5 ./...

build:
	go build -tags sqlite_fts5 -o bin/humanengine ./cmd/humanengine

plugin-layout-check: unbundled-plugin-graph-check external-only-plugin-graph-check
	python3 plugins/check-layout.py

# Check import paths, actual package paths and local module replacements.
SNOW_FREE_TAGS ?= sqlite_fts5,humanengine_icu
unbundled-plugin-graph-check:
	@deps=$$(go list -buildvcs=false -tags '$(SNOW_FREE_TAGS)' -deps -f '{{.ImportPath}} {{.Dir}}{{with .Module}}{{with .Replace}} {{.Dir}}{{end}}{{end}}' ./cmd/humanengine) || exit $$?; \
	if printf '%s\n' "$$deps" | grep -E '/plugins/snowluma(/|[[:space:]]|$$)'; then \
		echo 'SnowLuma-free command depends on SnowLuma' >&2; exit 1; \
	fi

# The external-only host must not statically link any plugin resource or policy.
external-only-plugin-graph-check:
	@deps=$$(go list -buildvcs=false -tags 'sqlite_fts5,humanengine_icu' -deps -f '{{.ImportPath}} {{.Dir}}{{with .Module}}{{with .Replace}} {{.Dir}}{{end}}{{end}}' ./cmd/humanengine) || exit $$?; \
	if printf '%s\n' "$$deps" | grep -E '/plugins/(typing-delay|web-read|web-search|snowluma|metadata)(/|[[:space:]]|$$)'; then \
		echo 'External-only command depends on a plugin' >&2; exit 1; \
	fi

clean: clean-wasm
	rm -f bin/humanengine

clean-wasm:
	rm -f $(foreach plugin,$(WASM_PLUGINS),plugins/$(plugin)/dist/$(plugin).wasm)

plugin-source-check: plugin-layout-check builtin-source-check
	cd .. && node --import tsx backend/internal/plugins/generate-configuration-errors.mjs --check
	cd .. && node --experimental-transform-types backend/internal/pluginmanager/generate-configuration-source.mjs --check
	cd .. && node --experimental-transform-types backend/internal/pluginmanager/generate-enablement-source.mjs --check
	cd .. && node --experimental-transform-types backend/internal/pluginmanager/generate-async-configuration-source.mjs --check

contextual-source-check:
	cd .. && TZ=UTC node --import tsx backend/tests/generate-contextual-learning-fixtures.ts --check

differential: build plugin-source-check contextual-source-check runtime-producer-source-check
	cd .. && TZ=UTC node --import tsx backend/tests/generate-memory-create.mjs --check
	cd .. && TZ=UTC node --import tsx backend/tests/generate-external-cost-read.mjs --check
	cd .. && TZ=UTC node --import tsx backend/tests/generate-external-cost-import.mjs --check
	cd .. && TZ=UTC node --import tsx backend/tests/generate-external-cost-correction.mjs --check
	cd .. && TZ=UTC node --import tsx backend/tests/generate-budget-ack.mjs --check
	cd .. && TZ=UTC node --import tsx backend/tests/generate-budget-read.mjs --check
	cd .. && TZ=UTC node --import tsx backend/tests/generate-memory-patch.mjs --check
	cd .. && TZ=UTC node --import tsx backend/tests/generate-memory-people.mjs --check
	cd .. && TZ=UTC node --import tsx backend/tests/generate-memory-relations.mjs --check
	cd .. && TZ=UTC node --import tsx backend/tests/generate-memory-knowledge.mjs --check
	cd .. && TZ=UTC node --import tsx backend/tests/generate-memory-history.mjs --check
	cd .. && TZ=UTC node --import tsx backend/tests/generate-memory-episodes.mjs --check
	cd .. && TZ=UTC node --import tsx backend/tests/generate-memory-semantics.mjs --check
	cd .. && TZ=UTC node --import tsx backend/tests/generate-memory-working-set.mjs --check
	cd .. && TZ=UTC node --import tsx backend/tests/generate-memory-working-set-pin.mjs --check
	cd .. && TZ=UTC node --import tsx backend/tests/generate-embedding-admin-status.mjs --check
	cd .. && TZ=UTC node --import tsx backend/tests/generate-embedding-action-boundary.mjs --check
	cd .. && TZ=UTC node --import tsx backend/tests/generate-embedding-rebuild-storage.mjs --check
	cd .. && TZ=UTC node --import tsx backend/tests/generate-embedding-rebuild-http.mjs --check
	cd .. && TZ=UTC node --import tsx backend/tests/generate-assertion-history.mjs --check
	cd .. && TZ=UTC node --import tsx backend/tests/generate-identity-admin-read.mjs --check
	cd .. && TZ=UTC node --import tsx backend/tests/generate-identity-admin-write.mjs --check
	cd .. && TZ=UTC node --import tsx backend/tests/generate-commitment-admin-read.mjs --check
	cd .. && TZ=UTC node --import tsx backend/tests/generate-live-usage.mjs --check
	cd .. && TZ=UTC node --import tsx backend/tests/generate-usage-retry.mjs --check
	cd .. && TZ=UTC node --import tsx backend/tests/generate-remote-history.mjs --check
	cd .. && TZ=UTC node backend/tests/generate-log-channel-memory.mjs --check
	cd .. && TZ=UTC node --import tsx backend/tests/generate-log-store-memory.mjs --check
	cd .. && TZ=UTC node --import tsx backend/tests/generate-log-status-http.mjs --check
	cd .. && TZ=UTC node --import tsx backend/tests/generate-log-status-headers.mjs --check
	cd .. && node --import tsx scripts/vision-oracle.ts --check
	cd .. && node --import tsx scripts/vision-errors.mjs --check
	cd .. && node --import tsx backend/internal/credentials/testdata/model-read-source.mjs --check
	cd .. && node --import tsx scripts/vision-dynamic.mjs --check
	cd .. && node --import tsx scripts/vision-errors-diagnostics.mjs --check
	cd .. && node --import tsx backend/internal/httpapi/testdata/meme-outgoing.mjs --check
	cd .. && node --import tsx backend/internal/memeengine/testdata/cached-guards.mjs --check
	cd .. && TZ=UTC node --import tsx backend/tests/generate-configuration-http.mjs --check
	cd .. && TZ=UTC node --import tsx backend/tests/generate-model-probe.mjs --check
	cd .. && TZ=UTC node --import tsx backend/tests/generate-rules-http.mjs --check
	cd .. && HE_SOURCE_ROOT="$$PWD" TZ=UTC node --import tsx backend/tests/generate-meme-http.mjs --check
	cd .. && HE_SOURCE_ROOT="$$PWD" node --import tsx backend/internal/modelread/testdata/source-oracle.mjs --check
	cd .. && HE_SOURCE_ROOT="$$PWD" node --import tsx backend/internal/imagecodec/testdata/display-sharp.mjs --check
	cd .. && TZ=UTC node --import tsx backend/tests/generate-managed-display-image.mjs --check
	cd .. && TZ=UTC node --import tsx backend/tests/generate-snowluma-inline-image.mjs --check
	cd .. && TZ=UTC node --import tsx backend/tests/generate-snowluma-token-public-return.mjs --check
	cd .. && TZ=UTC node --import tsx backend/tests/generate-snowluma-browser-403-refresh.mjs --check
	cd .. && node backend/internal/plugins/generate-v1-adapters.mjs --check
	cd .. && V1_SOURCE_ROOT="$$PWD" node --import tsx backend/internal/plugins/generate-v1-adapter-fixture.mjs --check
	cd .. && V1_SOURCE_ROOT="$$PWD" node --import tsx backend/internal/plugins/generate-v1-adapter-lifecycle-fixture.mjs --check
	cd .. && for script in generate-experience-http-fixtures generate-experience-http-presence generate-experience-http-reviewfix generate-experience-http-rootfix; do HE_SOURCE_ROOT="$$PWD" TZ=UTC node --import tsx backend/tests/$$script.mjs --check || exit $$?; done
	cd .. && HE_SOURCE_ROOT="$$PWD" TZ=UTC node --import tsx backend/tests/generate-experience-runtime-fixtures.mjs --check
	cd .. && HE_SOURCE_ROOT="$$PWD" TZ=UTC node --import tsx backend/tests/generate-diagnostics-http.mjs --check
	cd .. && HE_SOURCE_ROOT="$$PWD" TZ=UTC node --import tsx backend/tests/generate-modelcalls-http.mjs --check
	cd .. && HE_SOURCE_ROOT="$$PWD" TZ=UTC node --import tsx backend/tests/generate-router-lifetime.mjs --check
	cd .. && TZ=UTC node --import tsx backend/tests/generate-learning-admin.mjs --check
	cd .. && TZ=UTC node --import tsx backend/tests/generate-memory-list.mjs --check
	cd .. && TZ=UTC node backend/tests/generate-requested-locale.mjs --check
	cd .. && HE_SOURCE_ROOT="$$PWD" TZ=UTC node --import tsx backend/tests/generate-scenes-http.mjs --check
	cd .. && TZ=UTC node --import tsx backend/tests/generate-scene-messages.mjs --check
	cd .. && TZ=UTC node --import tsx backend/tests/generate-snowluma-retained-direct-links.mjs --check
	cd .. && TZ=UTC node --import tsx backend/tests/generate-snowluma-remote-browser-image.mjs --check
	cd .. && TZ=UTC node --import tsx backend/tests/generate-snowluma-opaque-browser-links.mjs --check
	cd .. && TZ=UTC node --import tsx backend/tests/generate-simulations.mjs --check
	cd .. && TZ=UTC node --import tsx backend/tests/generate-browser-session.mjs --check
	cd .. && HE_SOURCE_ROOT="$$PWD" TZ=UTC node --import tsx backend/tests/generate-experience-planner.mjs --check
	cd .. && TZ=UTC node backend/tests/check-meme-oracles.mjs
	cd .. && TZ=UTC node backend/tests/check-experience-oracles.mjs
	cd .. && HUMANENGINE_SOURCE_ROOT="$$PWD" node --import tsx backend/tests/generate-offline-lifecycle-fixtures.mjs --check
	$(PYTHON) tests/check-provider-fixtures.py
	$(PYTHON) tests/check-reconcile-fixtures.py
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
	cd .. && TZ=UTC node --import tsx backend/tests/generate-identity-exposure.mjs --check
	cd .. && TZ=UTC node --import tsx backend/tests/generate-historical-exposure.mjs --check
	cd .. && TZ=UTC node --import tsx backend/tests/generate-commitment-exposure.mjs --check
	cd .. && TZ=UTC node --import tsx backend/tests/generate-commitment-recorded-exposure.mjs --check
	cd .. && TZ=UTC node --import tsx backend/tests/generate-model-memory-exposure.mjs --check
	cd .. && TZ=UTC node --import tsx backend/internal/knowledge/testdata/generate-read-oracle.ts --check
	cd .. && TZ=UTC node --import tsx backend/internal/memoryservice/testdata/generate-episode-oracle.ts --check
	cd .. && TZ=UTC node --import tsx backend/internal/knowledge/testdata/generate-source-oracle.ts --check
	cd .. && TZ=UTC node --import tsx backend/tests/generate-semantic-purge-fixtures.ts --check
	cd .. && TZ=UTC node --import tsx backend/tests/generate-semantic-runtime-fixtures.ts --check
	cd .. && TZ=UTC node --import tsx backend/tests/generate-semantic-owner-fixtures.ts --check
	cd .. && TZ=UTC node --import tsx backend/tests/generate-learning-runtime-fixtures.ts --check
	cd .. && TZ=UTC node --import tsx backend/tests/generate-learning-batch-contract-fixtures.mjs --check
	cd .. && TZ=UTC node --import tsx backend/tests/generate-learning-batch-stage-fixtures.ts --check
	cd .. && TZ=UTC node --import tsx backend/tests/generate-learning-owner-fixtures.ts --check
	cd .. && TZ=UTC node --import tsx backend/tests/generate-learning-search-index-fixtures.ts --check
	cd .. && node backend/tests/generate-learning-unicode-fixtures.mjs --check
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
	cd .. && TZ=UTC node --import tsx backend/tests/generate-shared-memory-undefined.mjs --check
	cd .. && node --import tsx backend/tests/generate-scene-write-fixtures.mjs --check
	cd .. && node --import tsx backend/tests/generate-dispatch-fixtures.mjs --check
	cd .. && node backend/tests/generate-v1-compat.mjs --check
	cd .. && node --import tsx backend/tests/generate-scheduler-fixtures.mjs --check
	cd .. && node --import tsx backend/tests/generate-turnwait-fixtures.mjs --check
	cd .. && node --import tsx backend/tests/config-differential.mjs backend/bin/humanengine

WASM_PLUGINS := typing-delay web-read web-search snowluma

wasm:
	@set -e; for plugin in $(WASM_PLUGINS); do \
		mkdir -p "plugins/$$plugin/dist"; \
		CGO_ENABLED=0 GOOS=wasip1 GOARCH=wasm go build -trimpath -buildvcs=false -buildmode=c-shared -ldflags='-s -w' \
			-o "plugins/$$plugin/dist/$$plugin.wasm" "./plugins/$$plugin/wasm"; \
	done

bundle: wasm
	go build -trimpath -tags sqlite_fts5,bundled_wasm -o bin/humanengine ./cmd/humanengine
	cd .. && node backend/tests/bundled-source-free.mjs backend/bin/humanengine

test-bundle: wasm
	go test -tags sqlite_fts5,bundled_wasm ./internal/plugins -run TestDistributedArtifactsLoadWithoutSources
	go test -race -tags sqlite_fts5,bundled_wasm ./internal/httpapi -run '^TestTyping(Builtin|External)RealWASMAndPendingCancellation$$'

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

# The explicit launcher gate verifies exact execution, not just a green package
# with native acceptance accidentally absent. Both native invocations use the
# checkout and lockfile-installed dependencies provided by the CI job's npm ci.
NATIVE_TEST_ENV = REQUIRE_ICU=1 HE_SOURCE_ROOT="$(abspath ..)" HE_IMAGE_LIBRARY="$(IMAGE_LIBRARY)" HUMANENGINE_VIPS_LIBRARY="$(IMAGE_LIBRARY)"

# The uncached gate above runs every preview test. Keep the full app suite
# under race with headroom for the slower hosted runner.
NATIVE_RUNTIME_PREVIEW_TESTS := ^TestRuntimePreview
native-icu-test: native-runtime-preview-test
	. "$(ICU_WORK_DIR)/env.sh" && $(NATIVE_TEST_ENV) go test -timeout 20m -race -tags sqlite_fts5,humanengine_icu,bundled_wasm -skip '$(NATIVE_RUNTIME_PREVIEW_TESTS)' ./...
	. "$(ICU_WORK_DIR)/env.sh" && go vet -tags sqlite_fts5,humanengine_icu,bundled_wasm ./...

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

.PHONY: native-image-check
# Verification uses the lockfile-pinned official artifact already installed by
# npm ci. This target does not publish or bundle the shared object.
IMAGE_LIBRARY ?= $(abspath ../node_modules/@img/sharp-libvips-linux-x64/lib/libvips-cpp.so.8.18.7)
export HUMANENGINE_VIPS_LIBRARY ?= $(IMAGE_LIBRARY)
export HE_IMAGE_LIBRARY ?= $(IMAGE_LIBRARY)
IMAGE_PROOF_ROOT ?= $(or $(RUNNER_TEMP),$(TMPDIR),/tmp)/humanengine-image-proofs
native-image-check:
	cd .. && node backend/tests/check-meme-image-oracle.mjs
	$(PYTHON) tests/check-image-trace-test.py
	cd .. && HUMANENGINE_SOURCE_ROOT="$$PWD" node --import tsx backend/tests/generate-image-codec-fixtures.mjs --check
	cd .. && HUMANENGINE_SOURCE_ROOT="$$PWD" node --import tsx backend/tests/generate-image-boundary-fixtures.mjs --check
	HUMANENGINE_VIPS_LIBRARY="$(IMAGE_LIBRARY)" HUMANENGINE_RUN_LARGE_IMAGE_BOUNDARY=1 VIPS_CONCURRENCY=1 go test -race -tags sqlite_fts5,bundled_sqlite_vec ./internal/imagecodec ./internal/memeservice
	go vet -tags sqlite_fts5,bundled_sqlite_vec ./internal/imagecodec ./internal/memeservice ./cmd/imagecodec-probe
	@mkdir -p "$(IMAGE_PROOF_ROOT)" && out=$$(mktemp -d "$(IMAGE_PROOF_ROOT)/run.XXXXXXXX") && \
	  HUMANENGINE_VIPS_LIBRARY="$(IMAGE_LIBRARY)" HUMANENGINE_IMAGE_REQUIRE_TRACE=1 HUMANENGINE_IMAGE_PROOF_DIR="$$out/proof" sh tests/image-source-free.sh

# Keep the frozen migration baseline and add an actual current-host Node oracle.
.PHONY: habit-numeric-check
habit-numeric-check:
	@out=$$(mktemp -d) && trap 'rm -rf "$$out"' EXIT && \
	  node tests/generate-js-pow-fixtures.mjs --host-output "$$out/pow.json" && \
	  HE_POW_ORACLE="$$out/pow.json" go test -race -tags sqlite_fts5 ./internal/jsnumber

.PHONY: runtime-producer-source-check
# Extracted source contracts with synthetic fixtures; no Engine startup.
runtime-producer-source-check:
	cd .. && node backend/scripts/media-owner-source-oracle.mjs
	cd .. && node backend/scripts/proactive-owner-source-oracle.mjs

builtin-source-check:
	cd .. && TZ=UTC node --import tsx backend/plugins/snowluma/check-source.mjs --check
	cd .. && TZ=UTC node --import tsx backend/plugins/typing-delay/check-source.mjs --check
	cd .. && TZ=UTC node --import tsx backend/plugins/web-read/check-source.mjs --check
	cd .. && TZ=UTC node --import tsx backend/plugins/web-search/check-source.mjs --check

.PHONY: test-runtime-preview-profile native-runtime-preview-test
# Keep dependency-free refusal/bind/authority checks in the ordinary profile.
# Unset native fixture paths so a developer's shell cannot mask a profile leak.
test-runtime-preview-profile:
	@set -eu; log=$$(mktemp); trap 'rm -f "$$log"' EXIT; status=0; \
	  env -u HE_SOURCE_ROOT -u HE_IMAGE_LIBRARY -u REQUIRE_ICU go test -json -race -count=1 -tags sqlite_fts5 ./cmd/humanengine -run '^TestRuntimePreview' > "$$log" || status=$$?; \
	  cat "$$log"; test "$$status" = 0 && $(PYTHON) tests/check-runtime-preview-profile.py default "$$log"

# Native startup checks pinned libvips/sqlite-vec, real ICU and all builtins.
# Node resolution uses PATH populated by actions/setup-node; no fake executable.
native-runtime-preview-test: native-icu-build wasm
	@set -eu; . "$(ICU_WORK_DIR)/env.sh"; log=$$(mktemp); trap 'rm -f "$$log"' EXIT; status=0; \
	  $(NATIVE_TEST_ENV) go test -json -race -count=1 -tags sqlite_fts5,humanengine_icu,bundled_wasm ./cmd/humanengine -run '$(NATIVE_RUNTIME_PREVIEW_TESTS)' > "$$log" || status=$$?; \
	  cat "$$log"; test "$$status" = 0 && $(PYTHON) tests/check-runtime-preview-profile.py native "$$log"

# Opt-in external HTTP preview. The packaged pair is never tracked in Git.
SNOW_HTTP_PACKAGE_DIR ?= plugins/snowluma/dist/portable-http
.PHONY: wasm-snowluma-http-preview
wasm-snowluma-http-preview:
	mkdir -p "$(SNOW_HTTP_PACKAGE_DIR)"
	CGO_ENABLED=0 GOOS=wasip1 GOARCH=wasm go build -trimpath -buildvcs=false -buildmode=c-shared -ldflags='-s -w' \
		-o "$(SNOW_HTTP_PACKAGE_DIR)/snowluma-http.wasm" ./plugins/snowluma/portable-http
	python3 -c 'import hashlib,json,pathlib,sys; p=pathlib.Path(sys.argv[1]); m=json.loads(pathlib.Path("plugins/snowluma/portable-http/manifest.json").read_text()); m["artifactSha256"]=hashlib.sha256((p/m["artifact"]).read_bytes()).hexdigest(); (p/"snowluma-http.v2.json").write_text(json.dumps(m,indent=2)+"\n")' "$(SNOW_HTTP_PACKAGE_DIR)"

# Independent send-delay package. Emit the manifest and digest beside the guest.
TYPING_DELAY_PACKAGE_DIR ?= plugins/typing-delay/dist/external
.PHONY: wasm-typing-delay-package
wasm-typing-delay-package:
	mkdir -p "$(TYPING_DELAY_PACKAGE_DIR)"
	CGO_ENABLED=0 GOOS=wasip1 GOARCH=wasm go build -trimpath -buildvcs=false -buildmode=c-shared -ldflags='-s -w' \
		-o "$(TYPING_DELAY_PACKAGE_DIR)/typing-delay.wasm" ./plugins/typing-delay/wasm
	$(PYTHON) -c 'import hashlib,json,pathlib,sys; p=pathlib.Path(sys.argv[1]); m=json.loads(pathlib.Path("plugins/typing-delay/manifest.json").read_text()); m["artifact"]="typing-delay.wasm"; m["artifactSha256"]=hashlib.sha256((p/m["artifact"]).read_bytes()).hexdigest(); (p/"typing-delay.v2.json").write_text(json.dumps(m,indent=2)+"\n")' "$(TYPING_DELAY_PACKAGE_DIR)"

# Separate opt-in webpage GET guest, not the experimental structured/send guest.
WEB_READ_GET_PACKAGE_DIR ?= plugins/web-read/dist/public-get
.PHONY: wasm-web-read-get-package
wasm-web-read-get-package:
	mkdir -p "$(WEB_READ_GET_PACKAGE_DIR)"
	CGO_ENABLED=0 GOOS=wasip1 GOARCH=wasm go build -tags web_read_public_get -trimpath -buildvcs=false -buildmode=c-shared -ldflags='-s -w' \
		-o "$(WEB_READ_GET_PACKAGE_DIR)/web-read.wasm" ./plugins/web-read/wasm
	$(PYTHON) -c 'import hashlib,json,pathlib,sys; p=pathlib.Path(sys.argv[1]); m=json.loads(pathlib.Path("plugins/web-read/manifest.json").read_text()); m.update(artifact="web-read.wasm", toolWire="public-get-v1", description="Read public webpages and retrieve original bytes from the turn cache."); m["artifactSha256"]=hashlib.sha256((p/m["artifact"]).read_bytes()).hexdigest(); (p/"web-read.v2.json").write_text(json.dumps(m,indent=2)+"\n")' "$(WEB_READ_GET_PACKAGE_DIR)"

# Explicit external package. Generated outputs belong outside Git.
.PHONY: wasm-web-search-structured-package
wasm-web-search-structured-package:
	@test -n "$(WEB_SEARCH_STRUCTURED_PACKAGE_DIR)" || { echo "Set WEB_SEARCH_STRUCTURED_PACKAGE_DIR" >&2; exit 2; }
	mkdir -p "$(WEB_SEARCH_STRUCTURED_PACKAGE_DIR)"
	CGO_ENABLED=0 GOOS=wasip1 GOARCH=wasm go build -trimpath -buildvcs=false -buildmode=c-shared -ldflags='-s -w' -o "$(WEB_SEARCH_STRUCTURED_PACKAGE_DIR)/web-search.wasm" ./plugins/web-search/wasm
	$(PYTHON) -c 'import hashlib,json,pathlib,sys; p=pathlib.Path(sys.argv[1]); r=pathlib.Path("plugins/web-search"); m=json.loads((r/"manifest.json").read_text()); profile=json.loads((r/"structured-profile.json").read_text()); m["configSchema"]["properties"]={k:m["configSchema"]["properties"][k] for k in profile.pop("properties")}; m.update(profile,artifact="web-search.wasm"); m["artifactSha256"]=hashlib.sha256((p/m["artifact"]).read_bytes()).hexdigest(); (p/"web-search.v2.json").write_text(json.dumps(m,indent=2)+"\n")' "$(WEB_SEARCH_STRUCTURED_PACKAGE_DIR)"
