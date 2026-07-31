#!/bin/bash
# Regenerate the clone-command QR code in the top-level README.
#
#   ./scripts/make_qr.sh
#
# Generated locally with segno (BSD-licensed) rather than an online QR service,
# so the README carries a real image instead of a hotlink to a third party that
# may disappear, rate-limit, or track scans.
#
# Re-run this if CLONE_CMD below ever changes.

set -e

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO_ROOT"

# The single source of truth for what the QR encodes. Keep in sync with the
# "Following Along as a Student" section of README.md.
CLONE_CMD='git clone --branch gophercon-2026-workshop https://github.com/derekparker/gc26-concurrency-workshop.git'

OUT=assets/clone-qr.png

if ! command -v uv > /dev/null 2>&1; then
    echo "❌ uv is required to run segno without installing it globally."
    echo "   Install uv (https://docs.astral.sh/uv/) or generate the QR another way."
    exit 1
fi

mkdir -p assets

uv run --quiet --with segno python - "$CLONE_CMD" "$OUT" <<'PY'
import sys
import segno

content, out = sys.argv[1], sys.argv[2]
qr = segno.make(content, error="m")
# scale 8 keeps it legible on a projector without bloating the repo.
qr.save(out, scale=8, border=4, dark="#000000", light="#ffffff")
print(f"version {qr.version}, error level {qr.error}, {len(content)} chars encoded")
PY

echo "✅ wrote $OUT"
echo "   encodes: $CLONE_CMD"
echo
echo "Verify by scanning it before you rely on it in front of a room."
