#!/bin/sh
set -eu

REPO_BASE="https://packages.printmaster.work"
KEY_URL="$REPO_BASE/gpg.key"
KEYRING="/etc/apt/keyrings/printmaster.gpg"
SOURCE_FILE="/etc/apt/sources.list.d/printmaster.list"
EXPECTED_FINGERPRINT="41CB14AF15B82312DE5ED2EAF5CF692407FFF7DF"

if [ "$(id -u)" -ne 0 ]; then
    echo "Run this installer as root (for example: curl -fsSL $REPO_BASE/install.sh | sudo bash)." >&2
    exit 1
fi

if ! command -v apt-get >/dev/null 2>&1; then
    echo "This installer supports Debian and Ubuntu systems with apt-get." >&2
    exit 1
fi

ARCH="$(dpkg --print-architecture)"
case "$ARCH" in
    amd64|arm64) ;;
    *)
        echo "Unsupported Debian architecture: $ARCH (supported: amd64, arm64)." >&2
        exit 1
        ;;
esac

NEEDS_TOOLS=0
for tool in curl gpg; do
    if ! command -v "$tool" >/dev/null 2>&1; then
        NEEDS_TOOLS=1
    fi
done
if [ "$NEEDS_TOOLS" -eq 1 ]; then
    apt-get update -qq
    DEBIAN_FRONTEND=noninteractive apt-get install -y ca-certificates curl gnupg
fi

TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT HUP INT TERM

curl --fail --silent --show-error --location --proto '=https' --tlsv1.2 \
    "$KEY_URL" -o "$TMP_DIR/printmaster.asc"
ACTUAL_FINGERPRINT="$(gpg --show-keys --with-colons "$TMP_DIR/printmaster.asc" | awk -F: '$1 == "fpr" { print toupper($10); exit }')"
if [ "$ACTUAL_FINGERPRINT" != "$EXPECTED_FINGERPRINT" ]; then
    echo "PrintMaster signing-key fingerprint mismatch; refusing to configure APT." >&2
    exit 1
fi

gpg --batch --yes --dearmor --output "$TMP_DIR/printmaster.gpg" "$TMP_DIR/printmaster.asc"
install -d -m 0755 /etc/apt/keyrings
install -m 0644 "$TMP_DIR/printmaster.gpg" "$KEYRING"

# Replace legacy PrintMaster source entries so APT does not load duplicate or unsigned definitions.
rm -f "$SOURCE_FILE" /etc/apt/sources.list.d/printmaster.sources
printf 'deb [arch=%s signed-by=%s] %s stable main\n' \
    "$ARCH" "$KEYRING" "$REPO_BASE" > "$TMP_DIR/printmaster.list"
install -m 0644 "$TMP_DIR/printmaster.list" "$SOURCE_FILE"

apt-get update
DEBIAN_FRONTEND=noninteractive apt-get install -y printmaster-agent
