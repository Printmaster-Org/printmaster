#!/bin/bash
# build-deb.sh - Build Debian package for PrintMaster Agent
# Usage: ./build-deb.sh [version] [arch]
#
# This script creates a .deb package from a pre-built binary.
# Run this on Ubuntu/Debian after building the Go binary.

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
AGENT_DIR="$SCRIPT_DIR"

# Get version from VERSION file or argument
VERSION="${1:-$(cat "$AGENT_DIR/VERSION")}"
ARCH="${2:-amd64}"
PACKAGE_VERSION="${VERSION/-beta./~beta.}"

# Map Go arch to Debian arch
case "$ARCH" in
    amd64) DEB_ARCH="amd64" ;;
    arm64) DEB_ARCH="arm64" ;;
    arm)   DEB_ARCH="armhf" ;;
    *)     DEB_ARCH="$ARCH" ;;
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

echo "Building Debian package..."
echo "  Package: $PACKAGE_NAME"
echo "  Version: $VERSION"
echo "  Architecture: $DEB_ARCH"
echo "  Binary: $BINARY"

# Create build directory
BUILD_DIR="$AGENT_DIR/dist/deb-build"
PKG_DIR="$BUILD_DIR/${PACKAGE_NAME}_${VERSION}_${DEB_ARCH}"

rm -rf "$BUILD_DIR"
mkdir -p "$PKG_DIR/DEBIAN"
mkdir -p "$PKG_DIR/usr/bin"
mkdir -p "$PKG_DIR/lib/systemd/system"
mkdir -p "$PKG_DIR/etc/printmaster"
mkdir -p "$PKG_DIR/var/lib/printmaster"
mkdir -p "$PKG_DIR/var/log/printmaster"

# Copy binary
cp "$BINARY" "$PKG_DIR/usr/bin/printmaster-agent"
chmod 755 "$PKG_DIR/usr/bin/printmaster-agent"

# Create sudoers.d directory and file for package manager auto-update
mkdir -p "$PKG_DIR/etc/sudoers.d"
cat > "$PKG_DIR/etc/sudoers.d/printmaster-agent" << 'SUDOERS'
# PrintMaster Agent - Allow auto-update via package manager
# This file allows the printmaster service user to update only the printmaster-agent package
# Remove this file to disable automatic package updates

# Debian/Ubuntu apt-get commands
printmaster ALL=(root) NOPASSWD: /usr/bin/apt-get update -qq
printmaster ALL=(root) NOPASSWD: /usr/bin/apt-get install -y -qq --allow-downgrades printmaster-agent
printmaster ALL=(root) NOPASSWD: /usr/bin/apt-get install -y -qq --allow-downgrades ^printmaster-agent=[0-9][A-Za-z0-9._+~-]*$

# Allow agent to restart itself after update
printmaster ALL=(root) NOPASSWD: /usr/bin/systemctl restart --no-block printmaster-agent.service
printmaster ALL=(root) NOPASSWD: /bin/systemctl restart --no-block printmaster-agent.service
SUDOERS
chmod 440 "$PKG_DIR/etc/sudoers.d/printmaster-agent"

# Copy systemd service (modify path for /usr/bin)
sed 's|/usr/local/bin/printmaster-agent|/usr/bin/printmaster-agent|g' \
    "$AGENT_DIR/printmaster-agent.service" > "$PKG_DIR/lib/systemd/system/printmaster-agent.service"
# Update documentation URL
sed -i 's|github.com/yourorg/printmaster|github.com/printmaster-org/printmaster|g' \
    "$PKG_DIR/lib/systemd/system/printmaster-agent.service"
chmod 644 "$PKG_DIR/lib/systemd/system/printmaster-agent.service"

# Copy example config
cp "$AGENT_DIR/config.example.toml" "$PKG_DIR/etc/printmaster/agent.toml.example"
chmod 644 "$PKG_DIR/etc/printmaster/agent.toml.example"

# Calculate installed size (in KB)
INSTALLED_SIZE=$(du -sk "$PKG_DIR" | cut -f1)

# Create control file
cat > "$PKG_DIR/DEBIAN/control" << EOF
Package: $PACKAGE_NAME
Version: $PACKAGE_VERSION
Architecture: $DEB_ARCH
Maintainer: PrintMaster Team <printmaster@example.com>
Installed-Size: $INSTALLED_SIZE
Depends: libc6
Section: admin
Priority: optional
Homepage: https://github.com/printmaster-org/printmaster
Description: PrintMaster Agent - Network printer fleet management
 PrintMaster Agent discovers and monitors network printers via SNMP,
 collects metrics (page counts, toner levels, status), and optionally
 reports to a central PrintMaster Server for multi-site fleet management.
 .
 Features include auto-discovery via SNMP/mDNS/WS-Discovery, real-time
 printer status monitoring, supply level tracking, built-in web UI,
 and optional central server integration with WebSocket.
EOF

# Note: We don't create a conffiles entry because agent.toml is created
# by postinst from agent.toml.example, not shipped directly in the package.
# This allows dpkg to not track it as a conffile, giving users full control.

# Create postinst script
cat > "$PKG_DIR/DEBIAN/postinst" << 'POSTINST'
#!/bin/sh
set -e

case "$1" in
    configure)
        # Create printmaster user and group if they don't exist
        if ! getent group printmaster >/dev/null 2>&1; then
            groupadd --system printmaster
        fi
        if ! getent passwd printmaster >/dev/null 2>&1; then
            useradd --system --gid printmaster --home-dir /var/lib/printmaster \
                --shell /usr/sbin/nologin --comment "PrintMaster Agent" printmaster
        fi

        # Set ownership of data directories
        chown -R printmaster:printmaster /var/lib/printmaster
        chown -R printmaster:printmaster /var/log/printmaster
        chmod 750 /var/lib/printmaster
        chmod 750 /var/log/printmaster

        # Create default config from example if not exists
        if [ ! -f /etc/printmaster/agent.toml ]; then
            cp /etc/printmaster/agent.toml.example /etc/printmaster/agent.toml
            chown root:printmaster /etc/printmaster/agent.toml
            chmod 640 /etc/printmaster/agent.toml
        fi

        # Reload systemd and enable service
        if command -v systemctl >/dev/null 2>&1; then
            systemctl daemon-reload
            systemctl enable printmaster-agent.service || true
        fi
        
        echo ""
        echo "PrintMaster Agent installed successfully!"
        echo ""
        echo "To start the service:"
        echo "  sudo systemctl start printmaster-agent"
        echo ""
        echo "Configuration file: /etc/printmaster/agent.toml"
        echo "Web UI: http://localhost:8080"
        echo ""
        ;;
esac

exit 0
POSTINST
chmod 755 "$PKG_DIR/DEBIAN/postinst"

# Create prerm script
cat > "$PKG_DIR/DEBIAN/prerm" << 'PRERM'
#!/bin/sh
set -e

case "$1" in
    remove|upgrade|deconfigure)
        if command -v systemctl >/dev/null 2>&1; then
            if systemctl is-active --quiet printmaster-agent.service; then
                systemctl stop printmaster-agent.service || true
            fi
            if systemctl is-enabled --quiet printmaster-agent.service 2>/dev/null; then
                systemctl disable printmaster-agent.service || true
            fi
        fi
        ;;
esac

exit 0
PRERM
chmod 755 "$PKG_DIR/DEBIAN/prerm"

# Create postrm script
cat > "$PKG_DIR/DEBIAN/postrm" << 'POSTRM'
#!/bin/sh
set -e

case "$1" in
    purge)
        # Remove data directories and user on purge
        rm -rf /var/lib/printmaster
        rm -rf /var/log/printmaster
        rm -rf /etc/printmaster
        
        # Remove sudoers file
        rm -f /etc/sudoers.d/printmaster-agent
        
        # Remove user and group
        if getent passwd printmaster >/dev/null 2>&1; then
            userdel printmaster || true
        fi
        if getent group printmaster >/dev/null 2>&1; then
            groupdel printmaster || true
        fi
        
        # Reload systemd
        if command -v systemctl >/dev/null 2>&1; then
            systemctl daemon-reload || true
        fi
        ;;
esac

exit 0
POSTRM
chmod 755 "$PKG_DIR/DEBIAN/postrm"

# Build the package
DEB_FILE="$AGENT_DIR/dist/${PACKAGE_NAME}_${VERSION}_${DEB_ARCH}.deb"
mkdir -p "$AGENT_DIR/dist"

# Use dpkg-deb if available, otherwise fakeroot
if command -v dpkg-deb >/dev/null 2>&1; then
    dpkg-deb --build --root-owner-group "$PKG_DIR" "$DEB_FILE"
else
    echo "Error: dpkg-deb not found. Install dpkg-dev package."
    exit 1
fi

# Verify the package
if command -v lintian >/dev/null 2>&1; then
    echo ""
    echo "Running lintian (Debian package checker)..."
    lintian --no-tag-display-limit "$DEB_FILE" || true
fi

echo ""
echo "Package created successfully: $DEB_FILE"
echo ""
echo "To install locally:"
echo "  sudo dpkg -i $DEB_FILE"
echo ""
echo "To verify contents:"
echo "  dpkg -c $DEB_FILE"
