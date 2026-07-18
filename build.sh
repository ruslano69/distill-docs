#!/bin/bash
# Build script for distill-docs
# Builds: distill, distill-server, distilld
set -e
VERSION_BASE="0.1"
PATCH=$(git rev-list --count HEAD 2>/dev/null || echo "0")
VERSION="${VERSION_BASE}.${PATCH}"
LDFLAGS="-s -w -X github.com/ruslano69/distill-docs/internal/version.Version=${VERSION}"

echo "→ Building distill-docs v${VERSION}..."
go build -ldflags "${LDFLAGS}" -o distill        ./cmd/distill
echo "  ✓ distill"
go build -ldflags "${LDFLAGS}" -o distill-server ./cmd/distill-server
echo "  ✓ distill-server"
go build -ldflags "${LDFLAGS}" -o distilld       ./cmd/distilld
echo "  ✓ distilld"
echo ""
echo "✅ Built. Try:"
echo "  ./distill --db .knowledge/docs.sqlite init"
echo "  ./distill-server --root .distill publish --name 2026.07 --channel stable"
