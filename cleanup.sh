#!/bin/bash
# Remove generated workshop artifacts (traces, profiles, compiled binaries).
# Your code edits are left alone.

cd "$(dirname "$0")" || exit 1

echo "🧹 Cleaning up generated workshop files..."

# Remove all gitignored artifacts: *.trace, *.prof, debug binaries, etc.
git clean -fX

# Remove stray compiled exercise binaries (untracked executables from `go build`).
git ls-files --others --exclude-standard |
while IFS= read -r f; do
    if [ -x "$f" ] && file -b "$f" | grep -q 'executable'; then
        rm -f "$f"
        echo "Removing $f"
    fi
done

echo "✅ Cleanup complete"
echo "💡 To also discard your code edits and fully reset: git checkout -- ."
