#!/usr/bin/env bash

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$SCRIPT_DIR"
LOG_DIR="$PROJECT_ROOT/logs"
LOG_FILE="$LOG_DIR/build.log"

TARGET="agent"
RELEASE=0
VERBOSE_BUILD=0
INCREMENT_VERSION=0

if [[ ! -d "$LOG_DIR" ]]; then
  mkdir -p "$LOG_DIR"
fi

color_reset=$'\033[0m'
color_dim=$'\033[2m'
color_red=$'\033[31m'
color_yellow=$'\033[33m'
color_blue=$'\033[34m'

log() {
  local level="${2:-INFO}"
  local ts msg level_color
  msg="$1"
  ts="$(date +'%Y-%m-%dT%H:%M:%S%z')"
  case "$level" in
    ERROR) level_color="$color_red" ;;
    WARN) level_color="$color_yellow" ;;
    *) level_color="$color_blue" ;;
  esac
  printf '%b%s%b %b[%s]%b %s\n' "$color_dim" "$ts" "$color_reset" "$level_color" "$level" "$color_reset" "$msg"
  printf '[%s] [%s] %s\n' "$ts" "$level" "$msg" >> "$LOG_FILE"
}

usage() {
  cat <<EOF
Usage: ./build.sh [target] [options]
Targets: agent, server, both, all, clean, test, test-storage, test-all
Options: -Release, -VerboseBuild, -IncrementVersion
EOF
}

is_flag() {
  case "$1" in
    -Release|--release|-release|-VerboseBuild|--verbose-build|-verbose-build|-IncrementVersion|--increment-version|-increment-version) return 0 ;;
    *) return 1 ;;
  esac
}

for arg in "$@"; do
  if ! is_flag "$arg"; then
    TARGET="$arg"
    break
  fi
done

for arg in "$@"; do
  case "$arg" in
    -Release|--release|-release) RELEASE=1 ;;
    -VerboseBuild|--verbose-build|-verbose-build) VERBOSE_BUILD=1 ;;
    -IncrementVersion|--increment-version|-increment-version) INCREMENT_VERSION=1 ;;
  esac
done

case "$TARGET" in
  agent|server|both|all|clean|test|test-storage|test-all) ;;
  -h|--help|help)
    usage
    exit 0
    ;;
  *)
    usage
    exit 1
    ;;
esac

js_tests_passed="${PRINTMASTER_JS_TESTS_PASSED:-0}"
playwright_passed="${PRINTMASTER_PLAYWRIGHT_PASSED:-0}"
js_syntax_passed="${PRINTMASTER_JS_SYNTAX_PASSED:-0}"

ensure_cmd() {
  if ! command -v "$1" >/dev/null 2>&1; then
    log "$1 not found" ERROR
    exit 1
  fi
}

run_js_unit_tests() {
  if [[ "$js_tests_passed" == "1" ]]; then
    log "JS unit tests already passed this session, skipping"
    return 0
  fi
  if [[ "${PRINTMASTER_SKIP_JS_TESTS:-0}" == "1" ]]; then
    log "Skipping JS unit tests because PRINTMASTER_SKIP_JS_TESTS=1" WARN
    return 0
  fi
  log "Running JavaScript unit tests (jest)..."
  (cd "$PROJECT_ROOT" && npm run test:js)
  js_tests_passed=1
  export PRINTMASTER_JS_TESTS_PASSED=1
}

run_js_syntax_check() {
  if [[ "$js_syntax_passed" == "1" ]]; then
    log "JS syntax check already passed this session, skipping"
    return 0
  fi
  log "Running JS syntax check (node --check)"
  local found=0 failed=0 dir file
  for dir in "$PROJECT_ROOT/agent/web" "$PROJECT_ROOT/server/web" "$PROJECT_ROOT/common/web"; do
    [[ -d "$dir" ]] || continue
    while IFS= read -r -d '' file; do
      found=1
      log "Checking JS syntax: $file"
      if ! node --check "$file" >/dev/null 2>&1; then
        failed=1
        log "Syntax error in $file" ERROR
      fi
    done < <(find "$dir" -type f -name '*.js' -print0)
  done
  if [[ "$found" == "0" ]]; then
    log "No JS files found for syntax check" WARN
  fi
  if [[ "$failed" == "1" ]]; then
    log "JS syntax check failed" ERROR
    return 1
  fi
  js_syntax_passed=1
  export PRINTMASTER_JS_SYNTAX_PASSED=1
}

run_playwright_tests() {
  if [[ "$playwright_passed" == "1" ]]; then
    log "Playwright tests already passed this session, skipping"
    return 0
  fi
  if [[ "${PRINTMASTER_SKIP_PLAYWRIGHT:-0}" == "1" ]]; then
    log "Skipping Playwright tests because PRINTMASTER_SKIP_PLAYWRIGHT=1" WARN
    return 0
  fi
  log "Running Playwright smoke tests..."
  (cd "$PROJECT_ROOT" && npx playwright install >/dev/null 2>&1) || true
  (cd "$PROJECT_ROOT" && npm run test:playwright)
  playwright_passed=1
  export PRINTMASTER_PLAYWRIGHT_PASSED=1
}

test_prerequisites() {
  local go_path
  log "Checking build prerequisites..."
  ensure_cmd go
  ensure_cmd git
  ensure_cmd npm
  ensure_cmd npx
  if ! command -v staticcheck >/dev/null 2>&1; then
    log "staticcheck not found - installing..." WARN
    go install honnef.co/go/tools/cmd/staticcheck@latest
    go_path="$(go env GOPATH)"
    export PATH="$go_path/bin:$PATH"
  fi
  log "Found: $(go version)"
  log "Found staticcheck: $(staticcheck -version 2>/dev/null || echo installed)"
  log "Found: $(git --version)"
}

clean_artifacts() {
  log "Cleaning build artifacts..."
  find "$PROJECT_ROOT" -maxdepth 2 -type f \( \
    -name 'printmaster-agent' -o -name 'printmaster-server' -o \
    -name 'printmaster-agent.exe' -o -name 'printmaster-server.exe' -o \
    -name '*.syso' \) -print -delete || true
  log "Clean complete"
}

component_version_file() {
  printf '%s/%s/VERSION' "$PROJECT_ROOT" "$1"
}

component_build_files() {
  printf '%s/%s/.buildnumber\n%s/%s/.lastversion\n' "$PROJECT_ROOT" "$1" "$PROJECT_ROOT" "$1"
}

build_component() {
  local component="$1"
  local display_name output_name version_file version last_version build_number_file last_version_file build_number version_string build_type git_commit build_time ldflags out_dir build_args
  display_name="${component^}"
  out_dir="$PROJECT_ROOT/$component"
  output_name="printmaster-$component"
  version_file="$(component_version_file "$component")"
  build_number_file="$PROJECT_ROOT/$component/.buildnumber"
  last_version_file="$PROJECT_ROOT/$component/.lastversion"

  if [[ "$component" == "common" ]]; then
    log "Running linters for common..."
    pushd "$PROJECT_ROOT/common" >/dev/null
    go vet ./... 2>&1 | tee -a "$LOG_FILE"
    if command -v staticcheck >/dev/null 2>&1; then
      staticcheck ./... 2>&1 | tee -a "$LOG_FILE"
    fi
    go test ./... -v 2>&1 | tee -a "$LOG_FILE"
    popd >/dev/null
    return 0
  fi

  version="$(tr -d '\r\n' < "$version_file" 2>/dev/null || printf '0.0.0')"
  last_version="$(tr -d '\r\n' < "$last_version_file" 2>/dev/null || true)"

  if [[ "$RELEASE" == "1" && "$INCREMENT_VERSION" == "1" ]]; then
    if [[ "$version" =~ ^([0-9]+)\.([0-9]+)\.([0-9]+)$ ]]; then
      version="${BASH_REMATCH[1]}.${BASH_REMATCH[2]}.$((BASH_REMATCH[3] + 1))"
      printf '%s' "$version" > "$version_file"
      log "$display_name version incremented to: $version"
    else
      log "Invalid version format in VERSION file, expected x.y.z" WARN
    fi
  fi

  if [[ "$last_version" != "$version" ]]; then
    build_number=1
    log "Version changed from ${last_version:-<none>} to $version, resetting build number"
  else
    if [[ -f "$build_number_file" ]]; then
      build_number=$(( $(tr -d '\r\n' < "$build_number_file") + 1 ))
    else
      build_number=1
    fi
  fi
  printf '%s' "$build_number" > "$build_number_file"
  printf '%s' "$version" > "$last_version_file"

  if [[ "$RELEASE" == "1" ]]; then
    version_string="$version"
  else
    version_string="${version%%-*}.${build_number}-dev"
    local branch_suffix
    branch_suffix="${BRANCH_SUFFIX:-}"
    version_string+="$branch_suffix"
  fi

  log "=== Build Log ==="
  log "Component: $display_name"
  log "Version: $version_string"
  log "Build Number: $build_number"
  log "Log File: $(basename "$LOG_FILE")"

  if [[ "$component" == "agent" || "$component" == "server" ]]; then
    run_js_unit_tests
    run_playwright_tests
  fi
  run_js_syntax_check

  build_time="$(date -u +'%Y-%m-%dT%H:%M:%SZ')"
  git_commit="$(git rev-parse --short HEAD 2>/dev/null || echo unknown)"
  build_type="${RELEASE:+release}"
  [[ "$RELEASE" == "1" ]] || build_type="dev"
  if [[ "$RELEASE" == "1" && "$version" == *-beta.* ]]; then build_type="beta"; fi

  ldflags=(
    -X "main.Version=$version_string"
    -X "main.BuildTime=$build_time"
    -X "main.GitCommit=$git_commit"
    -X "main.BuildType=$build_type"
    -X "main.GitBranch=${GIT_BRANCH:-unknown}"
  )
  if [[ "$RELEASE" == "1" ]]; then
    ldflags+=( -s -w )
  fi

  build_args=(build)
  [[ "$RELEASE" == "1" ]] && build_args+=( -trimpath )
  build_args+=( -ldflags "${ldflags[*]}" )
  [[ "$VERBOSE_BUILD" == "1" ]] && build_args+=( -v )
  build_args+=( -o "$output_name" . )

  log "Version: $version_string${RELEASE:+}"
  log "Command: go ${build_args[*]}"
  log "Build Time: $build_time"
  log "Git Commit: $git_commit"

  pushd "$out_dir" >/dev/null
  if [[ "$component" == "agent" ]]; then
    CGO_ENABLED=0 go "${build_args[@]}" 2>&1 | tee -a "$LOG_FILE"
  else
    go "${build_args[@]}" 2>&1 | tee -a "$LOG_FILE"
  fi
  popd >/dev/null

  if [[ -f "$out_dir/$output_name" ]]; then
    log "SUCCESS: $output_name" INFO
  else
    log "Build reported success but executable not found" ERROR
    return 1
  fi
}

test_storage() {
  log "Running storage tests..."
  pushd "$PROJECT_ROOT/agent" >/dev/null
  go test ./storage -v 2>&1 | tee -a "$LOG_FILE"
  popd >/dev/null
}

test_all() {
  log "Running all agent tests..."
  pushd "$PROJECT_ROOT/agent" >/dev/null
  go test ./... -v 2>&1 | tee -a "$LOG_FILE"
  popd >/dev/null
}

main() {
  log "=== PrintMaster Build Script ==="
  log "Target: $TARGET"
  log "Project Root: $PROJECT_ROOT"
  test_prerequisites

  local git_branch_raw git_branch_sanitized
  git_branch_raw="$(git -C "$PROJECT_ROOT" rev-parse --abbrev-ref HEAD 2>/dev/null || echo unknown)"
  git_branch_sanitized="$(printf '%s' "$git_branch_raw" | tr -c 'A-Za-z0-9_.-' '-')"
  if [[ "$git_branch_sanitized" == "main" || "$git_branch_sanitized" == "master" || "$git_branch_sanitized" == "trunk" ]]; then
    BRANCH_SUFFIX=""
  else
    BRANCH_SUFFIX="-$git_branch_sanitized"
  fi
  export GIT_BRANCH="$git_branch_sanitized"

  case "$TARGET" in
    clean)
      clean_artifacts
      ;;
    agent)
      build_component common
      build_component agent
      ;;
    server)
      build_component common
      build_component server
      ;;
    both)
      build_component common
      build_component agent
      build_component server
      ;;
    all)
      build_component agent
      test_storage
      ;;
    test)
      test_storage
      ;;
    test-storage)
      test_storage
      ;;
    test-all)
      test_all
      ;;
  esac

  log "=== Build Complete ==="
  log "SUCCESS: Build script completed with exit code 0"
}

main "$@"
