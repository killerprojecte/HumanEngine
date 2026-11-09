#!/usr/bin/env python3
"""Check source ownership without requiring generated WASM binaries."""
from pathlib import Path
import json

backend = Path(__file__).resolve().parent.parent
plugins = backend / "plugins"
ids = ("typing-delay", "web-read", "web-search", "snowluma")
old_dirs = ("typingdelay", "webread", "websearch", "snowluma-wasm")
for name in old_dirs:
    assert not (plugins / name).exists(), f"split plugin directory: {name}"
assert not (backend / "internal/plugins/builtins").exists(), "shared artifact ownership returned"
assert not list((plugins / "metadata").glob("*.json")), "shared manifest ownership returned"
for name in ids:
    root = plugins / name
    for path in ("manifest.json", "manifest.go", "artifact_bundled.go", "wasm/main.go"):
        assert (root / path).is_file(), f"missing {name}/{path}"
    assert (root / "core").is_dir(), f"missing {name}/core"
    assert not (root / "main.go").exists(), f"root executable returned: {name}"
    manifest = json.loads((root / "manifest.json").read_text())
    assert manifest["id"] == name and manifest["apiVersion"] == 2
    embedding = (root / "artifact_bundled.go").read_text()
    assert embedding.startswith("//go:build bundled_wasm\n")
    assert f"//go:embed dist/{name}.wasm" in embedding
    assert f"/plugins/{name}/core" in (root / "wasm/main.go").read_text()
    for path in (root / "core").glob("*.go"):
        assert "/plugins/metadata" not in path.read_text(), f"aggregate dependency creates cycle: {path}"
assert (plugins / "typing-delay/host/delay.go").is_file(), "typing host ownership missing"
manager = (backend / "internal/pluginmanager/builtin.go").read_text()
assert "typinghost.Delay(" in manager
assert "time.NewTimer" not in manager and "invalid_typing_delay_policy" not in manager
for root in (backend, backend.parent / ".github"):
    for path in root.rglob("*"):
        if not path.is_file() or path == Path(__file__).resolve():
            continue
        if path.suffix not in (".go", ".mjs", ".py", ".md", ".yml", ".yaml") and path.name not in ("Makefile", ".gitignore"):
            continue
        text = path.read_text()
        forbidden = tuple("plugins/" + name for name in old_dirs) + ("internal/plugins/builtins",)
        for fragment in forbidden:
            assert fragment not in text, f"stale layout reference {fragment}: {path}"
print("PASS: four plugin-owned manifests, core packages, WASM entries, and tagged artifacts")
