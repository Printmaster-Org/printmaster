#!/bin/bash
# build-rpm.sh - Build RPM package for PrintMaster Agent
# Usage: ./build-rpm.sh [version] [arch]
#
# This script creates an .rpm package from a pre-built binary.
# Run this on Fedora/RHEL/CentOS after building the Go binary.

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
AGENT_DIR="$SCRIPT_DIR"

# Get version from VERSION file or argument
VERSION="${1:-$(cat "$AGENT_DIR/VERSION")}"
ARCH="${2:-amd64}"

# Map Go arch to RPM arch
case "$ARCH" in
    amd64) RPM_ARCH="x86_64" ;;
    arm64) RPM_ARCH="aarch64" ;;
    arm)   RPM_ARCH="armv7hl" ;;
    *)     RPM_ARCH="$ARCH" ;;
esac

PACKAGE_NAME="printmaster-agent"
BINARY_NAME="printmaster-agent"

# Find the binary (check multiple naming patterns)
BINARY=""
for pattern in \
    "$AGENT_DIR/$BINARY_NAME" \
    "$AGENT_DIR/${BINARY_NAME}-v${VERSION}-linux-${ARCH}" \
    "$AGENT_DIR/${BINARY_NAME}-linux-${ARCH}"; do
    if [ -f "$pattern" ]; then
        BINARY="$pattern"
        break
    fi
done

if [ -z "$BINARY" ] || [ ! -f "$BINARY" ]; then
    echo "Error: Cannot find agent binary. Looked for:"
    echo "  - $AGENT_DIR/$BINARY_NAME"
    echo "  - $AGENT_DIR/${BINARY_NAME}-v${VERSION}-linux-${ARCH}"
    echo "  - $AGENT_DIR/${BINARY_NAME}-linux-${ARCH}"
    exit 1
fi

echo "Building RPM package..."
echo "  Package: $PACKAGE_NAME"
echo "  Version: $VERSION"
echo "  Architecture: $RPM_ARCH"
echo "  Binary: $BINARY"

# Create RPM build directories
BUILD_ROOT="$AGENT_DIR/dist/rpm-build"
rm -rf "$BUILD_ROOT"
mkdir -p "$BUILD_ROOT"/{BUILD,RPMS,SOURCES,SPECS,SRPMS}

# Copy spec file
cp "$AGENT_DIR/fedora/printmaster-agent.spec" "$BUILD_ROOT/SPECS/"

# Export environment variables for spec file
export PRINTMASTER_VERSION="${VERSION/-beta./~beta.}"
PRINTMASTER_BINARY="$(realpath "$BINARY")"
export PRINTMASTER_BINARY

# Build the RPM
rpmbuild --define "_topdir $BUILD_ROOT" \
         --define "_arch $RPM_ARCH" \
         --target "$RPM_ARCH" \
         -bb "$BUILD_ROOT/SPECS/printmaster-agent.spec"

# Move RPM to dist directory
mkdir -p "$AGENT_DIR/dist"
while IFS= read -r package; do
    filename="$(basename "$package")"
    filename="${filename/printmaster-agent-$PRINTMASTER_VERSION-/printmaster-agent-$VERSION-}"
    cp "$package" "$AGENT_DIR/dist/$filename"
done < <(find "$BUILD_ROOT/RPMS" -name "*.rpm")

# List what we built
echo ""
echo "=== RPM Package Built ==="
ls -lh "$AGENT_DIR/dist/"*.rpm 2>/dev/null || echo "Warning: RPM file not found in expected location"

# Clean up build directory
rm -rf "$BUILD_ROOT"

echo ""
echo "Install with: sudo dnf install $AGENT_DIR/dist/${PACKAGE_NAME}*.rpm"
