#!/bin/bash

echo "🚀 Setting up Go Concurrency Workshop..."

# Minimum versions.
#
# Go 1.26: the exercise modules declare `go 1.26`.
#
# Delve 1.27.0: Delve enforces a supported Go range and hard-errors outside it.
#   Delve 1.26.0-1.26.3  supports Go 1.24-1.26
#   Delve 1.27.0         supports Go 1.25-1.27
# Only 1.27.0 covers Go 1.27, so require it regardless of the local Go version.
MIN_GO=1.26
MIN_DLV=1.27.0

# Comparable sort key for a dotted numeric version. Deliberately not `sort -V`,
# which orders "1.26" before "1.26rc1" and so lets release candidates satisfy a
# ">= 1.26" check even though an rc predates the final release.
ver_key() {
    local IFS=. major minor patch
    set -- $1
    major=${1:-0}; minor=${2:-0}; patch=${3:-0}
    printf '%05d%05d%05d' "$major" "$minor" "$patch"
}

ver_lt() { [ "$(ver_key "$1")" -lt "$(ver_key "$2")" ]; }

# ---------------------------------------------------------------- Go

if ! command -v go &> /dev/null; then
    echo "❌ Go is not installed. Please install Go $MIN_GO or later: https://go.dev/dl/"
    exit 1
fi

GO_RAW=$(go version | awk '{print $3}')     # e.g. go1.26.5, go1.26rc1, devel
GO_VERSION=${GO_RAW#go}

if [[ "$GO_RAW" == devel* || "$GO_VERSION" != [0-9]* ]]; then
    # A development/tip toolchain. Can't compare meaningfully; don't block.
    echo "⚠️  Go version: $GO_RAW (development build, skipping version check)"
else
    # Strip any pre-release suffix so the numeric part can be compared, and
    # remember that it was there: go1.26rc1 is *older* than go1.26.0.
    GO_NUM=$(printf '%s' "$GO_VERSION" | sed -E 's/(rc|beta|alpha).*$//')
    GO_PRE=""
    [[ "$GO_VERSION" != "$GO_NUM" ]] && GO_PRE=${GO_VERSION#"$GO_NUM"}

    # A pre-release only fails if the release it precedes isn't already past
    # the minimum. go1.26rc1 is older than go1.26.0 and must be rejected;
    # go1.27rc1 is newer than go1.26.0 and is fine (conference week may well
    # land on one).
    if [[ -n "$GO_PRE" ]] && ! ver_lt "$MIN_GO" "$GO_NUM"; then
        echo "❌ Go $GO_VERSION is a pre-release ($GO_PRE), which predates the final"
        echo "   Go $GO_NUM release. Please install a released Go $MIN_GO or later:"
        echo "   https://go.dev/dl/"
        exit 1
    fi
    if [[ -n "$GO_PRE" ]]; then
        echo "⚠️  Go $GO_VERSION is a pre-release, but newer than Go $MIN_GO. Continuing."
    fi

    if ver_lt "$GO_NUM" "$MIN_GO"; then
        echo "❌ Go $MIN_GO+ is required (found $GO_VERSION). Please upgrade: https://go.dev/dl/"
        exit 1
    fi

    echo "✅ Go version: $GO_VERSION"
fi

# ---------------------------------------------------------------- Delve

dlv_version() {
    # `dlv version` prints "Version: 1.27.0" on its own line.
    dlv version 2>/dev/null | awk '/^Version:/ {print $2; exit}'
}

install_delve() {
    echo "📦 Installing Delve $MIN_DLV+ ..."
    if ! go install github.com/go-delve/delve/cmd/dlv@latest; then
        echo "❌ Failed to install Delve. Install it manually:"
        echo "   go install github.com/go-delve/delve/cmd/dlv@latest"
        exit 1
    fi
    hash -r 2>/dev/null
}

if ! command -v dlv &> /dev/null; then
    install_delve
    if ! command -v dlv &> /dev/null; then
        echo "❌ Delve installed but not on your PATH. Add:"
        echo "   export PATH=\"\$PATH:\$(go env GOPATH)/bin\""
        exit 1
    fi
fi

DLV_VERSION=$(dlv_version)

if [[ -z "$DLV_VERSION" ]]; then
    echo "⚠️  Delve is installed but its version could not be determined."
    echo "   Verify manually that 'dlv version' reports $MIN_DLV or newer."
elif [[ "$DLV_VERSION" != [0-9]* ]]; then
    echo "⚠️  Delve version: $DLV_VERSION (development build, skipping version check)"
elif ver_lt "$DLV_VERSION" "$MIN_DLV"; then
    # Unlike the original script, an existing-but-stale install is not a pass.
    # Delve refuses to debug a Go version outside its supported range, so this
    # would otherwise surface as a hard error partway through Part III.
    echo "⚠️  Delve $DLV_VERSION is older than the required $MIN_DLV, upgrading..."
    install_delve
    DLV_VERSION=$(dlv_version)
    if [[ "$DLV_VERSION" == [0-9]* ]] && ver_lt "$DLV_VERSION" "$MIN_DLV"; then
        echo "❌ Delve is still $DLV_VERSION after upgrading."
        echo "   An older dlv earlier on your PATH is probably shadowing it:"
        echo "   $(command -v dlv)"
        echo "   Remove it, or put \$(go env GOPATH)/bin first on your PATH."
        exit 1
    fi
    echo "✅ Delve upgraded to $DLV_VERSION"
else
    echo "✅ Delve version: $DLV_VERSION"
fi

# Forward-looking: Delve caps the Go versions it will debug. If Go has moved
# ahead of what this script knows about, say so rather than let Part III fail.
if [[ "$GO_VERSION" == [0-9]* ]]; then
    GO_MINOR=$(printf '%s' "$GO_VERSION" | cut -d. -f2 | sed -E 's/(rc|beta|alpha).*$//')
    if [[ -n "$GO_MINOR" ]] && [ "$GO_MINOR" -gt 27 ] 2>/dev/null; then
        echo "⚠️  Go 1.$GO_MINOR is newer than the Go 1.27 that Delve $MIN_DLV supports."
        echo "   If 'dlv' reports \"too old for Go version\", upgrade Delve:"
        echo "   go install github.com/go-delve/delve/cmd/dlv@latest"
    fi
fi

# --------------------------------------------- Part III core-dump artifacts

# The linux/amd64 binary + core for the Part III post-mortem demo ship
# gzipped (2.2 MB instead of 51 MB). Unpack them if they aren't already.
CORE_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/03-delve/demo/linux-core"
if [ -d "$CORE_DIR" ]; then
    unpacked=0
    for gz in "$CORE_DIR"/pipeline.linux-amd64.gz \
              "$CORE_DIR"/pipeline.linux-amd64.core.gz; do
        [ -f "$gz" ] || continue
        target="${gz%.gz}"
        if [ ! -f "$target" ] || [ "$gz" -nt "$target" ]; then
            gunzip -kf "$gz" && unpacked=1
        fi
    done
    if [ -f "$CORE_DIR/pipeline.linux-amd64" ]; then
        chmod +x "$CORE_DIR/pipeline.linux-amd64"
    fi
    if [ "$unpacked" -eq 1 ]; then
        echo "✅ Unpacked Part III core-dump artifacts (03-delve/demo/linux-core/)"
    fi
fi

# ---------------------------------------------------------------- done

echo "✅ Workshop environment ready!"
echo ""
echo "📋 Quick verification:"
echo "   go version: $(go version)"
echo "   dlv version: $(dlv version 2>/dev/null | sed -n 2p || echo 'Not installed')"
echo ""
echo "🎯 You're ready for the workshop!"
echo ""
echo "Next steps:"
echo "  cd 01-race-detector/    # Part I starts here"
