#!/usr/bin/env bash
set -euo pipefail

artifact_dir="${1:?artifact directory required}"
version="${2:?development version required}"
cd "$artifact_dir"

# Download-artifact keeps each uploaded artifact in its own directory. Copy
# every distributable, not just the first find result; digests are not packages.
while IFS= read -r -d '' file; do
  cp "$file" "./$(basename "$file")"
done < <(find . -mindepth 2 -maxdepth 2 -type f -name 'printmaster-*' -print0)

# Dev publication must contain all five binaries built by the required matrix.
# Debian packages are included when supplied; MSI/RPM remain stable-tag builds.
for platform in linux-amd64 linux-arm64 windows-amd64 darwin-amd64 darwin-arm64; do
  extension=""
  if [[ "$platform" == windows-* ]]; then extension=".exe"; fi
  file="printmaster-agent-v${version}-${platform}${extension}"
  if [[ ! -s "$file" ]]; then
    echo "Missing or empty Agent dev binary: $file" >&2
    exit 1
  fi
done
echo "=== Agent Dev Release Assets ==="
ls -lh printmaster-*