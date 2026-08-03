#!/usr/bin/env bash
# Regenerate the committed linux/amd64 binary + core dump for the Part III
# post-mortem demo.
#
# Why these are committed at all: Delve's `dump` on macOS writes Go's entire
# reserved virtual arena, so this 6 MB program yields a ~6.4 GB core and
# about a minute of dead air, which is not something to do on stage. The
# same dump on Linux is 48 MB in 2.5 seconds. Cores are readable across OS
# and architecture, so one Linux core serves every attendee.
#
# Run this ON A LINUX MACHINE (or in a container, see below) from this
# directory. Requires Go 1.26+ and Delve 1.27+.
#
#   ./make-core.sh
#
# In a container from anywhere:
#
#   docker run --rm -v "$PWD/..:/src" -w /src/linux-core golang:1.26 \
#     bash -c 'go install github.com/go-delve/delve/cmd/dlv@latest && \
#              PATH=$PATH:/root/go/bin ./make-core.sh'
#
# -trimpath is what makes the recorded source path `delve-demo/pipeline.go`
# instead of an absolute build directory, so the demo can remap it with
#   (dlv) config substitute-path delve-demo ..
set -euo pipefail

cd "$(dirname "$0")"
SRC=".."
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

command -v dlv >/dev/null || { echo "dlv not on PATH" >&2; exit 1; }

case "$(uname -s)/$(uname -m)" in
    Linux/x86_64) ;;
    *) echo "⚠️  Expected Linux/x86_64, got $(uname -s)/$(uname -m)." >&2
       echo "   The committed artifacts are linux-amd64; regenerating here" >&2
       echo "   will produce a core for a different platform." >&2 ;;
esac

cp "$SRC/go.mod" "$SRC/pipeline.go" "$WORK/"
( cd "$WORK" && go build -trimpath -gcflags='all=-N -l' -o pipeline . )

printf 'continue\ndump %s/pipeline.core\nquit\ny\n' "$WORK" > "$WORK/init.txt"
( cd "$WORK" && dlv exec ./pipeline --init init.txt \
    --allow-non-terminal-interactive=true < /dev/null >/dev/null 2>&1 || true )

[ -s "$WORK/pipeline.core" ] || { echo "core dump was not produced" >&2; exit 1; }

gzip -9 -c "$WORK/pipeline"      > pipeline.linux-amd64.gz
gzip -9 -c "$WORK/pipeline.core" > pipeline.linux-amd64.core.gz

echo "✅ regenerated:"
ls -l pipeline.linux-amd64.gz pipeline.linux-amd64.core.gz
echo
echo "Provenance to record in README.md:"
echo "  $(go version)"
echo "  $(dlv version | sed -n 2p)"
