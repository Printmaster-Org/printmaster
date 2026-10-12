#!/bin/sh
# PrintMaster Agent installer: configures the signed package repository for the
# detected distro (apt, dnf/yum, zypper) and installs printmaster-agent.
set -eu

REPO_BASE="https://packages.printmaster.work"
KEY_URL="$REPO_BASE/gpg.key"
EXPECTED_FINGERPRINT="41CB14AF15B82312DE5ED2EAF5CF692407FFF7DF"

APT_KEYRING="/etc/apt/keyrings/printmaster.gpg"
APT_SOURCE_FILE="/etc/apt/sources.list.d/printmaster.list"
RPM_REPO_FILE="/etc/yum.repos.d/printmaster.repo"

die() {
    echo "$*" >&2
    exit 1
}

if [ "$(id -u)" -ne 0 ]; then
    die "Run this installer as root (for example: curl -fsSL $REPO_BASE/install.sh | sudo bash)."
fi

MACHINE="$(uname -m)"
case "$MACHINE" in
    x86_64|amd64) DEB_ARCH="amd64" ;;
    aarch64|arm64) DEB_ARCH="arm64" ;;
    *) die "Unsupported architecture: $MACHINE (supported: x86_64, aarch64)." ;;
esac

if command -v apt-get >/dev/null 2>&1; then
    PM="apt"
    DEB_ARCH="$(dpkg --print-architecture)"
    case "$DEB_ARCH" in
        amd64|arm64) ;;
        *) die "Unsupported Debian architecture: $DEB_ARCH (supported: amd64, arm64)." ;;
    esac
elif command -v dnf >/dev/null 2>&1; then
    PM="dnf"
elif command -v yum >/dev/null 2>&1; then
    PM="yum"
elif command -v zypper >/dev/null 2>&1; then
    PM="zypper"
    RPM_REPO_FILE="/etc/zypp/repos.d/printmaster.repo"
else
    die "Unsupported distribution: no apt-get, dnf, yum, or zypper found.
Download a release manually from https://github.com/printmaster-org/printmaster/releases"
fi

install_packages() {
    case "$PM" in
        apt)
            apt-get update -qq
            DEBIAN_FRONTEND=noninteractive apt-get install -y "$@"
            ;;
        dnf) dnf install -y "$@" ;;
        yum) yum install -y "$@" ;;
        zypper) zypper --non-interactive install "$@" ;;
    esac
}

# Install any missing prerequisites.
MISSING=""
command -v curl >/dev/null 2>&1 || MISSING="$MISSING curl"
command -v gpg >/dev/null 2>&1 || MISSING="$MISSING gnupg"
if [ -n "$MISSING" ]; then
    case "$PM" in
        apt) install_packages ca-certificates curl gnupg ;;
        zypper) install_packages ca-certificates curl gpg2 ;;
        *) install_packages ca-certificates curl gnupg2 ;;
    esac
fi

TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT HUP INT TERM

curl --fail --silent --show-error --location --proto '=https' --tlsv1.2 \
    "$KEY_URL" -o "$TMP_DIR/printmaster.asc"
ACTUAL_FINGERPRINT="$(gpg --show-keys --with-colons "$TMP_DIR/printmaster.asc" | awk -F: '$1 == "fpr" { print toupper($10); exit }')"
if [ "$ACTUAL_FINGERPRINT" != "$EXPECTED_FINGERPRINT" ]; then
    die "PrintMaster signing-key fingerprint mismatch; refusing to configure the repository."
fi

if [ "$PM" = "apt" ]; then
    gpg --batch --yes --dearmor --output "$TMP_DIR/printmaster.gpg" "$TMP_DIR/printmaster.asc"
    install -d -m 0755 /etc/apt/keyrings
    install -m 0644 "$TMP_DIR/printmaster.gpg" "$APT_KEYRING"

    # Replace legacy PrintMaster source entries so APT does not load duplicate or unsigned definitions.
    rm -f "$APT_SOURCE_FILE" /etc/apt/sources.list.d/printmaster.sources
    printf 'deb [arch=%s signed-by=%s] %s stable main\n' \
        "$DEB_ARCH" "$APT_KEYRING" "$REPO_BASE" > "$TMP_DIR/printmaster.list"
    install -m 0644 "$TMP_DIR/printmaster.list" "$APT_SOURCE_FILE"
else
    rpm --import "$TMP_DIR/printmaster.asc"
    install -d -m 0755 "$(dirname "$RPM_REPO_FILE")"
    cat > "$TMP_DIR/printmaster.repo" <<EOF
[printmaster]
name=PrintMaster Repository
baseurl=$REPO_BASE/rpm
enabled=1
gpgcheck=1
gpgkey=$KEY_URL
EOF
    install -m 0644 "$TMP_DIR/printmaster.repo" "$RPM_REPO_FILE"
fi

install_packages printmaster-agent

echo
echo "PrintMaster Agent installed. Start it with:"
echo "  systemctl enable --now printmaster-agent"
echo "Web UI: http://localhost:8080"
