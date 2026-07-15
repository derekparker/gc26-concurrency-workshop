#!/bin/bash

echo "🚀 Setting up Go Concurrency Workshop..."

# Check Go installation
if ! command -v go &> /dev/null; then
    echo "❌ Go is not installed. Please install Go 1.26 or later: https://go.dev/dl/"
    exit 1
fi

GO_VERSION=$(go version | awk '{print $3}' | sed 's/go//')
echo "✅ Go version: $GO_VERSION"

# Require Go 1.26+ (the exercise modules declare `go 1.26`)
if [[ $(printf '%s\n' "$GO_VERSION" "1.26" | sort -V | head -n1) != "1.26" ]]; then
    echo "❌ Go 1.26+ is required (found $GO_VERSION). Please upgrade: https://go.dev/dl/"
    exit 1
fi

# Install Delve if not present
if ! command -v dlv &> /dev/null; then
    echo "📦 Installing Delve debugger..."
    go install github.com/go-delve/delve/cmd/dlv@latest
    echo "✅ Delve installed (make sure \$(go env GOPATH)/bin is on your PATH)"
else
    echo "✅ Delve already installed"
fi

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
