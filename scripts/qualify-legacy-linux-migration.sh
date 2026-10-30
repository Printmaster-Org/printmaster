#!/usr/bin/env bash
set -euo pipefail

if [[ "${PRINTMASTER_LIVE_LEGACY_MIGRATION:-}" != disposable-github-runner ||
      "${GITHUB_ACTIONS:-}" != true || "${RUNNER_ENVIRONMENT:-}" != github-hosted ]]; then
    echo "Legacy migration requires an explicitly approved disposable GitHub runner." >&2
    exit 1
fi

action="${1:?expected fetch or run}"
version="${2:?expected legacy version}"
format="${3:?expected deb or rpm}"
source_dir="${4:?expected source directory}"
work="${5:?expected qualification directory}"
case "$version:$format" in
    0.30.5:deb) digest=8401dff90137a596911bfe36bd35158007fec3992f49a8a0ca06429a32076d9d ;;
    0.31.0:deb) digest=886c991ff24c66d486b085eced4d2f951bb9f28ffdc7f98fc982184a1623f198 ;;
    0.31.1:deb) digest=abf11273818443f64557d6da6b8ee7f5f65774682ca17bab9c5f857d39d406d0 ;;
    0.30.5:rpm) digest=e96210d5706c9b42882d8126ebab62d3f0509742efeb242b74d395e7131221bb ;;
    0.31.0:rpm) digest=86a267a68b9fd26235bc6b12c0b4104eee6e47e3ca1ff5d1246c5c94dce3df1a ;;
    0.31.1:rpm) digest=986e7fcfa90576a5bc1c8034b13d3a67300f7adb17a03ca92a0815867b7cfc92 ;;
    *) echo "Unsupported pinned legacy package: $version $format" >&2; exit 1 ;;
esac
mkdir -p "$work/evidence"
baseline="$work/baseline.$format"
if [[ "$action" == fetch ]]; then
    asset="printmaster-agent_${version}_amd64.deb"
    if [[ "$format" == rpm ]]; then
        fedora_release=fc44
        if [[ "$version" == 0.30.5 ]]; then fedora_release=fc43; fi
        asset="printmaster-agent-${version}-1.${fedora_release}.x86_64.rpm"
    fi
    curl --fail --location --proto '=https' --proto-redir '=https' --max-time 120 \
        --output "$baseline" "https://github.com/Printmaster-Org/printmaster/releases/download/agent-v${version}/${asset}"
    printf '%s  %s\n' "$digest" "$baseline" | sha256sum --check
    printf '%s  %s\n' "$digest" "$asset" > "$work/evidence/legacy-sha256.txt"
    exit 0
fi
if [[ "$action" != run || "$EUID" -ne 0 ]]; then
    echo "Native migration action requires run as root on the disposable runner." >&2
    exit 1
fi
# Recheck at the privileged boundary, not just before container/runner handoff.
printf '%s  %s\n' "$digest" "$baseline" | sha256sum --check
stage="$work/candidate"
mkdir -p "$stage"
cp "$source_dir/agent/build-$format.sh" "$source_dir/agent/printmaster-agent.service" \
    "$source_dir/agent/config.example.toml" "$stage/"
cp -r "$source_dir/agent/systemd" "$stage/"
cp "$work/agent" "$stage/printmaster-agent"
cp "$work/helper" "$stage/printmaster-agent-update-linux-amd64"
cp "$work/recovery" "$stage/printmaster-agent-update-recovery-linux-amd64"
if [[ "$format" == rpm ]]; then
    mkdir -p "$stage/fedora"
    cp "$source_dir/agent/fedora/printmaster-agent.spec" "$stage/fedora/"
fi
bash "$stage/build-$format.sh" 0.32.0 amd64
mapfile -t candidates < <(find "$stage/dist" -maxdepth 1 -name "*.$format" -type f)
if [[ "${#candidates[@]}" -ne 1 ]]; then
    echo "Expected exactly one candidate package" >&2
    exit 1
fi
cp "${candidates[0]}" "$work/candidate.$format"
sha256sum "$work/candidate.$format" > "$work/evidence/candidate-sha256.txt"
mkdir -p "$work/extracted"
if [[ "$format" == deb ]]; then
    dpkg-deb --extract "$work/candidate.deb" "$work/extracted"
else
    # RPM's standard build-root processing can transform ELF bytes. Compare
    # installation to the exact package payload, not its pre-packaging input.
    (cd "$work/extracted"; rpm2cpio "$work/candidate.rpm" | cpio --extract --make-directories --quiet './usr/bin/printmaster-agent')
fi
cp "$work/extracted/usr/bin/printmaster-agent" "$work/candidate-agent"
sha256sum "$work/agent" "$work/candidate-agent" > "$work/evidence/executable-sha256.txt"
PRINTMASTER_LIVE_LEGACY_MIGRATION_DIR="$work" \
PRINTMASTER_LIVE_LEGACY_MIGRATION_FORMAT="$format" \
PRINTMASTER_LIVE_LEGACY_MIGRATION_VERSION="$version" \
    "$work/migration.test" -test.v -test.timeout=8m '-test.run=^TestLiveLegacyLinuxMigration$' \
    2>&1 | tee "$work/evidence/migration.txt"
