#!/usr/bin/env bash

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
MESSAGE=""
PUSH=0

usage() {
  cat <<EOF
Usage: ./commit.sh <message> [--push]
  <message> must be a Conventional Commit, e.g. "fix(scanner): handle SNMP timeout"
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --push|-p)
      PUSH=1
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      if [[ -z "$MESSAGE" ]]; then
        MESSAGE="$1"
      else
        MESSAGE+=" $1"
      fi
      shift
      ;;
  esac
done

if [[ -z "$MESSAGE" ]]; then
  usage
  exit 1
fi

echo "=== PrintMaster Commit Script ==="
echo
echo "[1/3] Building agent and server..."
"$SCRIPT_DIR/build.sh" both
echo "Build succeeded!"
echo
echo "[2/3] Staging and committing changes..."
git -C "$SCRIPT_DIR" add -A
git -C "$SCRIPT_DIR" commit -m "$MESSAGE"
echo "Committed successfully!"
echo
if [[ "$PUSH" == "1" ]]; then
  echo "[3/3] Pushing to remote..."
  git -C "$SCRIPT_DIR" push
  echo "Pushed successfully!"
else
  echo "[3/3] Skipping push (use --push to push automatically)"
fi
echo
echo "=== Done ==="
