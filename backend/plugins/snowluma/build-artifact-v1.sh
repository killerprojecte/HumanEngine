#!/bin/sh
# Version 1: reproducible, reviewed Snow release artifact. Run from backend/.
set -eu
cd "$(dirname "$0")/../.."
export GOTOOLCHAIN=local CGO_ENABLED=0 GOOS=wasip1 GOARCH=wasm
case "$(go version)" in 'go version go1.27.3 '*) ;; *) echo 'Snow artifact v1 requires Go 1.27.3' >&2; exit 1;; esac
sha256sum -c plugins/snowluma/artifact_inputs.sha256
mkdir -p plugins/snowluma/dist
go build -trimpath -buildvcs=false -buildmode=c-shared -ldflags='-s -w' -o plugins/snowluma/dist/snowluma.wasm ./plugins/snowluma/wasm
python3 - <<'PY'
import hashlib,json
from pathlib import Path
p=Path('plugins/snowluma');manifest=json.loads((p/'artifact_manifest.json').read_text());artifact=(p/'dist/snowluma.wasm').read_bytes()
assert len(artifact)==manifest['bytes']
assert hashlib.sha256(artifact).hexdigest()==manifest['sha256'], 'new artifact differs: review and update artifact/manifest together, never silently use old bytes'
print('Snow release artifact v1 verified:',manifest['sha256'])
PY
