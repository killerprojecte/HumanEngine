"""Identify the exact four packages used by the operator candidate proofs."""
from hashlib import sha256
import json


PACKAGES = {
    "snowluma": "snowluma-http.v2.json",
    "typing-delay": "typing-delay.v2.json",
    "web-read": "web-read.v2.json",
    "web-search": "web-search.v2.json",
}


def load_packages(directory):
    packages = {}
    for plugin, filename in PACKAGES.items():
        raw = (directory / filename).read_bytes()
        manifest = json.loads(raw)
        artifact = manifest["artifact"]
        if (manifest["id"] != plugin or manifest["apiVersion"] != 2
                or artifact != filename.removesuffix(".v2.json") + ".wasm"):
            raise ValueError("invalid operator package")
        digest = sha256((directory / artifact).read_bytes()).hexdigest()
        if digest != manifest["artifactSha256"]:
            raise ValueError("operator artifact does not match its manifest")
        packages[plugin] = {"manifest": sha256(raw).hexdigest(), "artifact": digest}
    return packages
