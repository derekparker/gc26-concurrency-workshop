#!/bin/bash
# Serve the workshop slide decks with presenter notes enabled.
#
#   ./slides.sh          # serve on 127.0.0.1:3999
#   ./slides.sh 4000     # serve on a different port

set -e

REPO_ROOT="$(cd "$(dirname "$0")" && pwd)"
cd "$REPO_ROOT"

PORT="${1:-3999}"

if ! command -v present > /dev/null 2>&1; then
    echo "present not found — installing golang.org/x/tools/cmd/present..."
    go install golang.org/x/tools/cmd/present@latest
    export PATH="$PATH:$(go env GOPATH)/bin"
fi

if ! command -v present > /dev/null 2>&1; then
    echo
    echo "Install succeeded but 'present' still isn't on your PATH."
    echo "Add this to your shell profile:"
    echo "    export PATH=\"\$PATH:\$(go env GOPATH)/bin\""
    exit 1
fi

cat <<EOF

  Workshop decks:
    Part I    http://127.0.0.1:$PORT/01-race-detector/part1-race-detector.slide
    Part II   http://127.0.0.1:$PORT/02-execution-tracer/part2-execution-tracer.slide
    Part III  http://127.0.0.1:$PORT/03-delve/part3-delve.slide

  In the browser:  N = presenter notes    F = full screen    Esc = overview
  (allow pop-ups for 127.0.0.1:$PORT so the notes window can open)

EOF

# -play=false: nothing in these decks is meant to be run from the browser, and
# the playground would execute deck code as your user.
exec present -notes -play=false -http="127.0.0.1:$PORT" .
