#!/usr/bin/env bash
# Resolves the companion component release shown in a GitHub release body
# (the Agent for a Server release, the Server for an Agent release) and
# renders its install section as Markdown.
#
# Usage: release-companion.sh <companion: agent|server> <release-version>
#
# Selection: the same-version companion tag wins, so a Beta pair released
# together (`release both --beta`) links its Beta companion. Otherwise the
# newest Stable companion in the same major.minor line is used. Dev and other
# Beta tags are never chosen as a fallback.
#
# Output (GITHUB_OUTPUT format): <companion>_tag, <companion>_version,
# <companion>_is_beta and a multi-line <companion>_section.
set -euo pipefail

companion="${1:?companion component required}"
release_version="${2:?release version required}"
case "$companion" in agent|server) ;; *) echo "Invalid companion component: $companion" >&2; exit 1 ;; esac

repo_url="https://github.com/printmaster-org/printmaster"
image="ghcr.io/printmaster-org/printmaster-server"
major_minor="$(echo "${release_version%%-*}" | cut -d. -f1,2)"

tag=""
if git rev-parse -q --verify "refs/tags/$companion-v$release_version" >/dev/null; then
  tag="$companion-v$release_version"
else
  tag="$(git tag -l "$companion-v${major_minor}.*" --sort=-version:refname |
    grep -E "^$companion-v[0-9]+\.[0-9]+\.[0-9]+$" | head -n 1 || true)"
fi

version="${tag#"$companion-v"}"
is_beta=false
[[ "$version" == *-beta.* ]] && is_beta=true
if [[ -n "$tag" ]]; then
  echo "Companion $companion release: $tag (beta=$is_beta)" >&2
else
  echo "No companion $companion release for v$release_version or v${major_minor}.*" >&2
fi

download() { printf '%s/releases/download/%s/%s' "$repo_url" "$tag" "$1"; }
row() { printf '| %s | [%s](%s) |\n' "$1" "$2" "$(download "$2")"; }

agent_section() {
  if [[ -z "$tag" ]]; then
    printf 'No PrintMaster Agent release matches v%s yet. See [all releases](%s/releases).\n' "$major_minor" "$repo_url"
    return
  fi
  printf 'You will need the PrintMaster Agent to discover and monitor printers.\n\n'
  printf '**Matching Agent: v%s** — [View Release](%s/releases/tag/%s)\n\n' "$version" "$repo_url" "$tag"
  if [[ "$is_beta" == true ]]; then
    printf '**Beta Agent:** download the versioned assets below. The APT/DNF repositories and MSI installers carry Stable only.\n\n'
  else
    printf '**Debian/Ubuntu (APT repo, signed):**\n```bash\ncurl -fsSL https://packages.printmaster.work/install.sh | sudo bash\n```\n\n'
    printf '**Fedora/RHEL (DNF repo):**\n```bash\nsudo dnf config-manager addrepo --from-repofile=https://packages.printmaster.work/printmaster.repo && sudo dnf install -y printmaster-agent\n```\n\n'
  fi
  # RPM names carry a release/dist suffix (e.g. 0.31.1-1.fc44), so read the
  # real asset name when the GitHub CLI can see the companion release.
  # RELEASE_COMPANION_OFFLINE=1 skips the lookup (tests, local previews).
  local rpm=""
  if [[ "${RELEASE_COMPANION_OFFLINE:-}" != 1 ]] && command -v gh >/dev/null 2>&1; then
    rpm="$(gh release view "$tag" --repo "${GITHUB_REPOSITORY:-printmaster-org/printmaster}" --json assets \
      -q '.assets[].name | select(endswith(".x86_64.rpm"))' 2>/dev/null | head -n 1 || true)"
  fi
  printf '| Platform | Download |\n|----------|----------|\n'
  [[ "$is_beta" == true ]] || row 'Windows (MSI)' "printmaster-agent-v$version-windows-amd64.msi"
  row 'Windows (amd64)' "printmaster-agent-v$version-windows-amd64.exe"
  row 'Linux (amd64)' "printmaster-agent-v$version-linux-amd64"
  row 'Linux (arm64)' "printmaster-agent-v$version-linux-arm64"
  row 'Debian/Ubuntu (amd64)' "printmaster-agent_${version}_amd64.deb"
  row 'Debian/Ubuntu (arm64)' "printmaster-agent_${version}_arm64.deb"
  if [[ -n "$rpm" ]]; then
    row 'Fedora/RHEL' "$rpm"
  else
    printf '| Fedora/RHEL | [RPM packages](%s/releases/tag/%s) |\n' "$repo_url" "$tag"
  fi
  row 'macOS (Intel)' "printmaster-agent-v$version-darwin-amd64"
  row 'macOS (Apple Silicon)' "printmaster-agent-v$version-darwin-arm64"
}

server_section() {
  if [[ -z "$tag" ]]; then
    printf 'No PrintMaster Server release matches v%s yet. See [all releases](%s/releases).\n' "$major_minor" "$repo_url"
    return
  fi
  printf 'For multi-site management, you will also need the PrintMaster Server.\n\n'
  printf '**Matching Server: v%s** — [View Release](%s/releases/tag/%s)\n\n' "$version" "$repo_url" "$tag"
  printf '```bash\n# Docker (recommended; linux/amd64 and linux/arm64)\ndocker pull %s:%s\n```\n' "$image" "$version"
}

printf '%s\n' "${companion}_tag=$tag" "${companion}_version=$version" "${companion}_is_beta=$is_beta"
echo "${companion}_section<<PM_COMPANION_EOF"
"${companion}_section"
echo "PM_COMPANION_EOF"
