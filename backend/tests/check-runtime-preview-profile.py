#!/usr/bin/env python3
"""Fail closed when a required launcher test is missing, skipped or unsuccessful."""

import argparse
from collections import Counter
import json
from pathlib import Path

PACKAGE = "github.com/killerprojecte/HumanEngine/backend/cmd/humanengine"
DEFAULT = {
    "TestRuntimePreviewProcess",
    "TestRuntimePreviewBindFailureDoesNotEnroll",
    "TestRuntimePreviewModeConflictsAndDefaultRefusal",
    "TestRuntimePreviewPreflightFailureDoesNotEnroll",
}
NATIVE = DEFAULT | {
    "TestRuntimePreviewTCPBootWriteSignalDrainRestart",
    "TestRuntimePreviewNativePreflightFailureDoesNotEnroll",
    "TestRuntimePreviewNativePreflightFailureDoesNotEnroll/node",
    "TestRuntimePreviewNativePreflightFailureDoesNotEnroll/vector",
    "TestRuntimePreviewUnauthorizedHTTP",
    "TestRuntimePreviewSecondWriterRejectedFirstRemainsUsable",
}


def check(profile, events):
    expected = Counter({name: 1 for name in (DEFAULT if profile == "default" else NATIVE)})
    started, passed = Counter(), Counter()
    package_passes = 0
    for event in events:
        if event.get("Package") != PACKAGE:
            raise ValueError("unexpected package in launcher gate")
        action, test = event.get("Action"), event.get("Test")
        if action in ("skip", "fail"):
            raise ValueError(f"required launcher profile {action}: {test or PACKAGE}")
        if action == "run" and test:
            started[test] += 1
        if action == "pass":
            if test:
                passed[test] += 1
            else:
                package_passes += 1
    if started != expected or passed != expected or package_passes != 1:
        raise ValueError(
            f"launcher profile {profile} execution mismatch: "
            f"expected={dict(expected)}, started={dict(started)}, "
            f"passed={dict(passed)}, package_passes={package_passes}"
        )


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("profile", choices=("default", "native"))
    parser.add_argument("log", type=Path)
    args = parser.parse_args()
    try:
        events = [json.loads(line) for line in args.log.read_text().splitlines() if line.strip()]
        check(args.profile, events)
    except (OSError, ValueError, TypeError, AttributeError) as error:
        parser.exit(1, f"runtime-preview profile gate failed: {error}\n")
    print(f"runtime-preview {args.profile} profile: exact required tests passed; zero skips")


if __name__ == "__main__":
    main()
