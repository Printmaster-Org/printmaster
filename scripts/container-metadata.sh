#!/usr/bin/env bash
set -euo pipefail

component="${1:?component required}"
version="${2:?version required}"
build_type="${3:?build type required}"
revision="${4:?revision required}"
created="${5:?creation time required}"
case "$component" in
  agent) title="PrintMaster Agent"; summary="Site-local printer discovery, SNMP metrics, and fleet reporting." ;;
  server) title="PrintMaster Server"; summary="Central multi-site printer fleet monitoring, dashboards, and Agent management." ;;
  *) echo "Invalid component: $component" >&2; exit 1 ;;
esac
[[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-beta\.[1-9][0-9]*|-dev\.[0-9a-f]+)?$ ]] ||
  { echo "Invalid container version: $version" >&2; exit 1; }
case "$build_type" in
  release) channel=stable; [[ "$version" != *-* ]] || { echo "Stable version has a prerelease suffix" >&2; exit 1; } ;;
  beta) channel=beta; [[ "$version" == *-beta.* ]] || { echo "Beta version requires beta suffix" >&2; exit 1; } ;;
  dev) channel=dev; [[ "$version" == *-dev.* ]] || { echo "Dev version requires dev suffix" >&2; exit 1; } ;;
  *) echo "Invalid build type: $build_type" >&2; exit 1 ;;
esac
[[ "$revision" =~ ^[0-9a-f]{40}$ ]] || { echo "Full Git revision required" >&2; exit 1; }
[[ "$created" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$ ]] ||
  { echo "UTC creation time required" >&2; exit 1; }

documentation="https://docs.printmaster.work/deployment/docker/"
release_url="https://github.com/Printmaster-Org/printmaster/releases/tag/$component-v$version"
description="$summary Stable for production; Beta for testing; Dev for developers. Docs: $documentation Release notes: $release_url"
[[ ${#description} -le 512 ]] || { echo "GHCR description exceeds 512 characters" >&2; exit 1; }
printf '%s\n' \
  "org.opencontainers.image.title=$title" \
  "org.opencontainers.image.description=$description" \
  "org.opencontainers.image.source=https://github.com/Printmaster-Org/printmaster" \
  "org.opencontainers.image.documentation=$documentation" \
  "org.opencontainers.image.url=$release_url" \
  "org.opencontainers.image.licenses=MIT" \
  "org.opencontainers.image.version=$version" \
  "org.opencontainers.image.revision=$revision" \
  "org.opencontainers.image.created=$created" \
  "work.printmaster.image.channel=$channel"
