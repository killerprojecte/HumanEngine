.PHONY: test build differential wasm bundle test-bundle

test:
	go test -race ./...
	go vet ./...

build:
	go build -o bin/humanengine ./cmd/humanengine

differential: build
	cd .. && node --import tsx backend/tests/generate-turnwait-fixtures.mjs --check
	cd .. && node --import tsx backend/tests/config-differential.mjs backend/bin/humanengine

wasm:
	mkdir -p internal/plugins/builtins
	GOOS=wasip1 GOARCH=wasm go build -trimpath -buildmode=c-shared -ldflags='-s -w' -o internal/plugins/builtins/typing-delay.wasm ./plugins/typing-delay
	GOOS=wasip1 GOARCH=wasm go build -trimpath -buildmode=c-shared -ldflags='-s -w' -o internal/plugins/builtins/web-read.wasm ./plugins/web-read
	GOOS=wasip1 GOARCH=wasm go build -trimpath -buildmode=c-shared -ldflags='-s -w' -o internal/plugins/builtins/web-search.wasm ./plugins/web-search
	GOOS=wasip1 GOARCH=wasm go build -trimpath -buildmode=c-shared -ldflags='-s -w' -o internal/plugins/builtins/snowluma.wasm ./plugins/snowluma-wasm

bundle: wasm
	go build -trimpath -tags bundled_wasm -o bin/humanengine ./cmd/humanengine
	cd .. && node backend/tests/bundled-source-free.mjs backend/bin/humanengine

test-bundle: wasm
	go test -tags bundled_wasm ./internal/plugins -run TestDistributedArtifactsLoadWithoutSources
