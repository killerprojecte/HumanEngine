#!/bin/sh
# Run as the image's normal node user, inside its PID namespace. Never inspect
# argv/environ or spawn Node to observe Node. comm is not identity (MainThread).
set -eu
node_executable=$(readlink -f "$(command -v node)")
test -x "$node_executable"
for executable in /proc/[0-9]*/exe; do
    # Unrelated users and processes that exited during the scan are not ours.
    target=$(readlink "$executable" 2>/dev/null) || continue
    if [ "$target" = "$node_executable" ]; then
        pid=${executable#/proc/}
        printf '%s\n' "${pid%/exe}"
    fi
done
