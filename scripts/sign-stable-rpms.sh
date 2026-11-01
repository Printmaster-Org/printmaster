#!/usr/bin/env bash
set -euo pipefail

artifact_dir="${1:?expected RPM artifact directory}"
if [[ -z "${GPG_PRIVATE_KEY:-}" || -z "${GPG_PASSPHRASE:-}" ]]; then
    echo "Stable RPM signing requires the repository signing key and passphrase." >&2
    exit 1
fi
shopt -s nullglob
packages=("$artifact_dir"/*.rpm)
if [[ "${#packages[@]}" -ne 1 ]]; then
    echo "Expected exactly one RPM package in $artifact_dir, found ${#packages[@]}." >&2
    exit 1
fi

GNUPGHOME="$(mktemp -d)"
export GNUPGHOME RPM_GPG_PASSPHRASE="$GPG_PASSPHRASE"
trap 'rm -rf "$GNUPGHOME"' EXIT
chmod 0700 "$GNUPGHOME"
printf '%s' "$GPG_PRIVATE_KEY" | gpg --batch --import
fingerprint="$(gpg --batch --with-colons --list-secret-keys | awk -F: '$1 == "fpr" { print $10; exit }')"
if [[ ! "$fingerprint" =~ ^[A-Fa-f0-9]{40,64}$ ]]; then
    echo "No valid private signing key fingerprint was imported." >&2
    exit 1
fi

wrapper="$GNUPGHOME/rpm-gpg-wrapper"
cat > "$wrapper" <<'EOF'
#!/bin/sh
exec /usr/bin/gpg --batch --pinentry-mode loopback --passphrase "${RPM_GPG_PASSPHRASE:-}" "$@"
EOF
chmod 0700 "$wrapper"
printf '%%_openpgp_sign_id %s\n%%__gpg %s\n' "$fingerprint" "$wrapper" > "$HOME/.rpmmacros"
public_key="$GNUPGHOME/public.asc"
gpg --batch --armor --export "$fingerprint" > "$public_key"
rpm --import "$public_key"

for package in "${packages[@]}"; do
    rpmsign --addsign "$package"
    verification="$(rpm --checksig --verbose "$package")"
    printf '%s\n' "$verification"
    if ! grep -Fqi "$fingerprint" <<< "$verification" || ! grep -Eiq 'signature.*: OK' <<< "$verification"; then
        echo "Stable RPM signature verification failed: $(basename "$package")" >&2
        exit 1
    fi
done
