#!/usr/bin/env bash
set -euo pipefail

component="${1:?component required}"
ref="${2:?git ref required}"
case "$component" in agent|server) ;; *) echo "Invalid component: $component" >&2; exit 1 ;; esac
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
stored="$(tr -d '\r\n' < "$root/$component/VERSION")"
[[ "$stored" =~ ^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-beta\.([1-9][0-9]*))?$ ]] ||
  { echo "Invalid VERSION: $stored" >&2; exit 1; }
commit="$(git -C "$root" rev-parse --short HEAD)"
is_release=false
is_beta=false
if [[ "$ref" == "refs/tags/$component-v"* ]]; then
  version="${ref#refs/tags/$component-v}"
  [[ "$version" == "$stored" ]] || { echo "Tag version $version does not match VERSION $stored" >&2; exit 1; }
  if [[ "$version" == *-beta.* ]]; then
    build_type=beta
    is_beta=true
  else
    build_type=release
    is_release=true
  fi
else
  version="${stored%%-*}-dev.$commit"
  build_type=dev
fi
printf '%s\n' "version=$version" "${component}_version=$version" "tag=$component-v$version" \
  "build_type=$build_type" "is_release=$is_release" "is_beta=$is_beta" \
  "git_commit=$commit" "build_time=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
