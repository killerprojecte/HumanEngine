#!/usr/bin/env bash
set -euo pipefail

if [[ $GITHUB_EVENT_NAME != push || $GITHUB_REF != refs/heads/feat/refactor-golang ]]; then
  printf 'browser=false\nrestore=false\ncompose=false\n'
  exit
fi

if [[ ! $BEFORE =~ ^[0-9a-f]{40}$ ]]; then
  echo "Missing push base" >&2
  exit 1
fi
if [[ $BEFORE == 0000000000000000000000000000000000000000 ]]; then
  printf 'browser=false\nrestore=false\ncompose=false\n'
  exit
fi

trailers=$(git show -s --format='%(trailers:only)' "$GITHUB_SHA")
if grep -Fx 'HumanEngine-Package-Proofs: all' <<< "$trailers" > /dev/null; then
  printf 'browser=true\nrestore=true\ncompose=true\n'
  exit
fi

if ! git cat-file -e "$BEFORE^{commit}"; then
  git fetch --no-tags --depth=1 origin "$BEFORE"
fi
detect() {
  local key="$1" changed=0
  shift
  git diff --quiet "$BEFORE" "$GITHUB_SHA" -- "$@" || changed=$?
  case $changed in
    0) printf '%s=false\n' "$key" ;;
    1) printf '%s=true\n' "$key" ;;
    *) return "$changed" ;;
  esac
}

root=backend/packaging/runtime-preview
detect browser "$root/browser_smoke.py" "$root/browser-requirements.txt"
detect restore "$root/migration_drill.py" "$root/test_migration_drill.py"
detect compose "$root/compose-rehearsal.yaml" "$root/compose_rehearsal.py" "$root/test_compose_rehearsal.py" backend/internal/testsupport/snowstop
