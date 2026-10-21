#!/usr/bin/env bash
set -euo pipefail

if [[ "${PRINTMASTER_LIVE_LINUX_PACKAGES:-}" != disposable-github-runner ||
      "${GITHUB_ACTIONS:-}" != true || "${RUNNER_ENVIRONMENT:-}" != github-hosted ]]; then
    echo "Native package/service qualification requires an explicitly approved disposable GitHub runner." >&2
    exit 1
fi

format="${1:?expected deb or rpm}"
source_dir="${2:?expected source directory}"
work="${3:?expected qualification directory}"
case "$format" in deb|rpm) ;; *) echo "Invalid package format" >&2; exit 1 ;; esac
mkdir -p "$work/packages" "$work/evidence"

for version in 1.0.0 1.1.0; do
    stage="$work/package-$version"
    mkdir -p "$stage"
    cp "$source_dir/agent/build-$format.sh" "$source_dir/agent/printmaster-agent.service" \
        "$source_dir/agent/config.example.toml" "$stage/"
    cp -r "$source_dir/agent/systemd" "$stage/"
    cp "$work/agent-$version" "$stage/printmaster-agent"
    cp "$work/helper" "$stage/printmaster-agent-update-linux-amd64"
    cp "$work/recovery" "$stage/printmaster-agent-update-recovery-linux-amd64"
    if [[ "$format" == rpm ]]; then
        mkdir -p "$stage/fedora"
        cp "$source_dir/agent/fedora/printmaster-agent.spec" "$stage/fedora/"
    fi
    if [[ "$version" == 1.0.0 ]]; then
        # Reproduce the old RPM config-file ownership while granting only a
        # harmless fixture command. The candidate must revoke this grant.
        printf 'printmaster ALL=(root) NOPASSWD: /usr/bin/true\n' > "$stage/systemd/printmaster-agent-update.sudoers"
        if [[ "$format" == rpm ]]; then
            sed -i 's|^%attr(440,root,root) %{_sysconfdir}/sudoers.d/printmaster-agent$|%config(noreplace) %attr(440,root,root) %{_sysconfdir}/sudoers.d/printmaster-agent|' "$stage/fedora/printmaster-agent.spec"
        fi
    fi
    bash "$stage/build-$format.sh" "$version" amd64
    find "$stage/dist" -maxdepth 1 -name "*.$format" -exec cp {} "$work/packages/" \;
done

stage="$work/package-dependency"
cp -r "$work/package-1.1.0" "$stage"
if [[ "$format" == deb ]]; then
    sed -i 's/^Depends: libc6$/Depends: libc6, printmaster-qualification-missing (= 99)/' "$stage/build-deb.sh"
else
    sed -i '/^Requires:       glibc/a Requires:       printmaster-qualification-missing = 99' "$stage/fedora/printmaster-agent.spec"
fi
bash "$stage/build-$format.sh" 1.1.0 amd64
mkdir -p "$work/dependency"
find "$stage/dist" -maxdepth 1 -name "*.$format" -exec cp {} "$work/dependency/" \;

PRINTMASTER_LIVE_LINUX_PACKAGE_DIR="$work" \
PRINTMASTER_LIVE_LINUX_PACKAGE_FORMAT="$format" \
    "$work/updater.test" -test.v -test.timeout=10m '-test.run=^TestLiveLinuxPackageServiceLifecycle$'
