#!/bin/bash
# Remove generated workshop artifacts (traces, profiles, compiled binaries).
# Your code edits are left alone.

cd "$(dirname "$0")" || exit 1

echo "🧹 Cleaning up generated workshop files..."

# Remove generated artifacts by explicit pattern.
#
# We deliberately do NOT use `git clean -fX` here. -X means "delete everything
# git ignores", and git's ignore status is not a synonym for "generated
# artifact": .gitignore also carries local presenter tooling, which -X would
# happily delete. Two things that look like fixes but are not:
#   * -e/--exclude ADDS to the ignore rules, so under -X it makes a file MORE
#     eligible for deletion, not protected.
#   * a pathspec (`git clean -fX -- '*.trace'`) makes ignored *directories*
#     match, so docs/ and .claude/ get removed wholesale.
# Matching by name is boring and predictable, which is what we want here.
find . -path ./.git -prune -o -type f \( \
        -name '*.trace' -o -name '*.prof' -o -name '*.out' \
        -o -name '*.test' -o -name '__debug_bin*' \
    \) -print -delete

# Gitignored generated files, by name. Anything listed in .gitignore is
# invisible to the untracked sweep below (`--exclude-standard`), so these
# have to be named explicitly or they survive forever.
#   * the unpacked Part III core artifacts (setup.sh regenerates them from
#     the committed .gz files)
#   * the Part III demo binaries, gitignored to keep `git status` quiet
rm -f 03-delve/demo/linux-core/pipeline.linux-amd64 \
      03-delve/demo/linux-core/pipeline.linux-amd64.core \
      03-delve/demo/pipeline \
      03-delve/demo/delve-demo

# Remove stray compiled exercise binaries (untracked executables from `go build`).
# Match Mach-O/ELF only: `file` describes shell scripts as "... text executable"
# too, so a bare 'executable' grep silently deletes untracked helper scripts.
git ls-files --others --exclude-standard |
while IFS= read -r f; do
    if [ -x "$f" ] && file -b "$f" | grep -qE 'Mach-O|ELF'; then
        rm -f "$f"
        echo "Removing $f"
    fi
done

echo "✅ Cleanup complete"
echo "💡 To also discard your code edits and fully reset: git checkout -- ."
