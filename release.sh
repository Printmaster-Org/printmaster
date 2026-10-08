#!/usr/bin/env bash

set -euo pipefail
set -o errtrace

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$SCRIPT_DIR"

COMPONENT=""
BUMP_TYPE=""
MESSAGE=""
SKIP_TESTS=0
SKIP_PUSH=0
CREATE_GITHUB_RELEASE=0
FAIL_ON_EMPTY_CHANGELOG=1
DRY_RUN=0
BETA=0
RELEASE_COMMITTED=0
VERSIONS_CHANGED=0

usage() {
  cat <<EOF
Usage: ./release.sh <agent|server|both> <patch|minor|major|beta|stable> [message]
Use --beta with patch/minor/major to start a Beta cycle, beta to advance it, stable to promote it.
Options: --beta, --skip-tests, --skip-push, --create-github-release, --fail-on-empty-changelog=false, --dry-run
EOF
}

if [[ $# -lt 2 ]]; then
  usage
  exit 1
fi

COMPONENT="$1"
BUMP_TYPE="$2"
shift 2

case "$COMPONENT" in agent|server|both) ;; *) usage; exit 1 ;; esac
case "$BUMP_TYPE" in patch|minor|major|beta|stable) ;; *) usage; exit 1 ;; esac

while [[ $# -gt 0 ]]; do
  case "$1" in
    --skip-tests) SKIP_TESTS=1 ;;
    --skip-push) SKIP_PUSH=1 ;;
    --create-github-release) CREATE_GITHUB_RELEASE=1 ;;
    --fail-on-empty-changelog=false) FAIL_ON_EMPTY_CHANGELOG=0 ;;
    --dry-run) DRY_RUN=1 ;;
    --beta) BETA=1 ;;
    -m|--message)
      shift
      MESSAGE="${1:-}"
      ;;
    *)
      if [[ -z "$MESSAGE" ]]; then
        MESSAGE="$1"
      else
        MESSAGE+=" $1"
      fi
      ;;
  esac
  shift || true
done

color_reset=$'\033[0m'
color_dim=$'\033[2m'
color_red=$'\033[31m'
color_yellow=$'\033[33m'
color_blue=$'\033[34m'

status() {
  local msg="$1" level="${2:-INFO}" color="$color_blue" display_level
  display_level="$level"
  case "$level" in
    ERROR) color="$color_red" ;;
    WARN) color="$color_yellow" ;;
    STEP) display_level="INFO" ;;
  esac
  printf '%b%s%b %b[%s]%b %s\n' "$color_dim" "$(date +'%Y-%m-%dT%H:%M:%S%z')" "$color_reset" "$color" "$display_level" "$color_reset" "$msg"
}

on_error() {
  local exit_code=$? line="${BASH_LINENO[0]}" command="$BASH_COMMAND"
  status "Release failed at line $line: $command (exit $exit_code)" ERROR
  if [[ "$DRY_RUN" == "0" && "$RELEASE_COMMITTED" == "0" && "$VERSIONS_CHANGED" == "1" ]]; then
    status "Reverting VERSION file changes..." WARN
    case "$COMPONENT" in
      both) git -C "$PROJECT_ROOT" restore --source=HEAD --staged --worktree agent/VERSION server/VERSION || status "VERSION rollback failed; inspect git status" ERROR ;;
      server) git -C "$PROJECT_ROOT" restore --source=HEAD --staged --worktree server/VERSION || status "VERSION rollback failed; inspect git status" ERROR ;;
      agent) git -C "$PROJECT_ROOT" restore --source=HEAD --staged --worktree agent/VERSION || status "VERSION rollback failed; inspect git status" ERROR ;;
    esac
  elif [[ "$RELEASE_COMMITTED" == "1" ]]; then
    status "Release commit already exists; VERSION files were not reverted" WARN
  fi
  status "Fix the issue and try again" WARN
  exit "$exit_code"
}

git_status() { git -C "$PROJECT_ROOT" status --porcelain; }
git_clean() { [[ -z "$(git_status)" ]]; }

update_version() {
  local version_file="$1" bump_type="$2" current major minor patch new_version beta_number
  current="$(tr -d '\r\n' < "$version_file")"
  [[ "$current" =~ ^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-beta\.([1-9][0-9]*))?$ ]] || { echo "Invalid version format in $version_file: $current" >&2; return 1; }
  major="${BASH_REMATCH[1]}"; minor="${BASH_REMATCH[2]}"; patch="${BASH_REMATCH[3]}"
  beta_number="${BASH_REMATCH[5]:-}"
  if [[ "$bump_type" == "beta" || "$bump_type" == "stable" ]]; then
    [[ -n "$beta_number" && "$BETA" == "0" ]] || { echo "$bump_type requires an existing Beta version and no --beta flag" >&2; return 1; }
  else
    [[ -z "$beta_number" ]] || { echo "Use beta to advance or stable to promote the current Beta cycle" >&2; return 1; }
  fi
  case "$bump_type" in
    major) major=$((major + 1)); minor=0; patch=0 ;;
    minor) minor=$((minor + 1)); patch=0 ;;
    patch) patch=$((patch + 1)) ;;
  esac
  new_version="$major.$minor.$patch"
  if [[ "$bump_type" == "beta" ]]; then
    new_version+="-beta.$((beta_number + 1))"
  elif [[ "$BETA" == "1" ]]; then
    new_version+="-beta.1"
  fi
  git -C "$PROJECT_ROOT" show-ref --verify --quiet "refs/tags/$(basename "$(dirname "$version_file")")-v$new_version" &&
    { echo "Release tag already exists for $new_version" >&2; return 1; }
  if [[ "$DRY_RUN" == "0" ]]; then
    printf '%s' "$new_version" > "$version_file"
  fi
  printf '%s\n%s\n' "$current" "$new_version"
}

build_component() {
  local component="$1" version="$2"
  status "Building $component..." STEP
  if [[ -n "$version" ]]; then
    if [[ "$MESSAGE" ]]; then :; fi
  fi
  if [[ "$component" == "both" ]]; then
    :
  fi
}

build_release_binary() {
  local component="$1"
  status "Building $component..." STEP
  if [[ "$DRY_RUN" == "1" ]]; then
    status "[DRY RUN] Would build $component" WARN
    return 0
  fi
  "$PROJECT_ROOT/build.sh" "$component" --release
}

invoke_tests() {
  local component="$1"
  if [[ "$DRY_RUN" == "1" ]]; then
    status "[DRY RUN] Would test $component" WARN
    return 0
  fi
  if [[ "$SKIP_TESTS" == "1" ]]; then
    status "Skipping tests (--skip-tests flag)" WARN
    return 0
  fi
  status "Running tests for $component..." STEP
  (cd "$PROJECT_ROOT/$component" && go test ./... -v)
}

get_changelog_since_last_tag() {
  local component="$1" tag_pattern last_tag commit_range line message hash
  tag_pattern="${component}-v*"
  last_tag="$(git -C "$PROJECT_ROOT" tag -l "$tag_pattern" --sort=-version:refname | grep -E "^${component}-v[0-9]+\.[0-9]+\.[0-9]+$" | head -n 1 || true)"
  if [[ -z "$last_tag" ]]; then
    commit_range="HEAD"
  else
    commit_range="$last_tag..HEAD"
  fi
  local features=() fixes=() docs=() chores=() refactors=() tests=() other=()
  while IFS= read -r line; do
    [[ -z "$line" ]] && continue
    message="${line%%|||*}"
    hash="${line##*|||}"
    if [[ "$message" == feat:* || "$message" == feat\(*\):* ]]; then
      features+=("- ${message#*: } ($hash)")
    elif [[ "$message" == fix:* || "$message" == fix\(*\):* ]]; then
      fixes+=("- ${message#*: } ($hash)")
    elif [[ "$message" == docs:* || "$message" == docs\(*\):* ]]; then
      docs+=("- ${message#*: } ($hash)")
    elif [[ "$message" == chore:* || "$message" == chore\(*\):* ]]; then
      chores+=("- ${message#*: } ($hash)")
    elif [[ "$message" == refactor:* || "$message" == refactor\(*\):* ]]; then
      refactors+=("- ${message#*: } ($hash)")
    elif [[ "$message" == test:* || "$message" == test\(*\):* ]]; then
      tests+=("- ${message#*: } ($hash)")
    else
      other+=("- $message ($hash)")
    fi
  done < <(git -C "$PROJECT_ROOT" log "$commit_range" --pretty=format:'%s|||%h' --no-merges 2>/dev/null || true)
  if [[ ${#features[@]} -eq 0 && ${#fixes[@]} -eq 0 && ${#docs[@]} -eq 0 && ${#chores[@]} -eq 0 && ${#refactors[@]} -eq 0 && ${#tests[@]} -eq 0 && ${#other[@]} -eq 0 ]]; then
    git -C "$PROJECT_ROOT" log "$commit_range" --pretty=format:'- %s (%h)' --no-merges 2>/dev/null || true
    return 0
  fi
  {
    [[ ${#features[@]} -gt 0 ]] && { printf '### Features\n\n'; printf '%s\n' "${features[@]}"; printf '\n'; }
    [[ ${#fixes[@]} -gt 0 ]] && { printf '### Bug Fixes\n\n'; printf '%s\n' "${fixes[@]}"; printf '\n'; }
    [[ ${#refactors[@]} -gt 0 ]] && { printf '### Refactoring\n\n'; printf '%s\n' "${refactors[@]}"; printf '\n'; }
    [[ ${#docs[@]} -gt 0 ]] && { printf '### Documentation\n\n'; printf '%s\n' "${docs[@]}"; printf '\n'; }
    [[ ${#tests[@]} -gt 0 ]] && { printf '### Tests\n\n'; printf '%s\n' "${tests[@]}"; printf '\n'; }
    [[ ${#chores[@]} -gt 0 ]] && { printf '### Maintenance\n\n'; printf '%s\n' "${chores[@]}"; printf '\n'; }
    [[ ${#other[@]} -gt 0 ]] && { printf '### Other Changes\n\n'; printf '%s\n' "${other[@]}"; printf '\n'; }
  }
  return 0
}

is_changelog_meaningful() {
  local text="$1"
  [[ -n "$text" ]] || return 1
  [[ "$text" =~ ^-\  || "$text" =~ ^### || ${#text} -gt 80 ]]
}

save_commit_and_tag() {
  local component="$1" version="$2" commit_sha commit_msg agent_ver server_ver
  status "Committing version bump..." STEP
  if [[ "$DRY_RUN" == "1" ]]; then
    status "[DRY RUN] Would commit VERSION files" WARN
    if [[ "$component" == "both" ]]; then
      status "[DRY RUN] Would tag as agent-v${agent_versions[1]} and server-v${server_versions[1]}" WARN
    else
      status "[DRY RUN] Would tag as $component-v$version" WARN
    fi
    return 0
  fi
  if [[ "$version" == *-beta.* ]]; then
    local components=("$component") item item_version
    [[ "$component" != "both" ]] || components=(agent server)
    local files=()
    for item in "${components[@]}"; do files+=("$item/VERSION"); done
    git -C "$PROJECT_ROOT" add "${files[@]}"
    commit_msg="${MESSAGE:-chore: Release Beta}"
    for item in "${components[@]}"; do
      item_version="$(tr -d '\r\n' < "$PROJECT_ROOT/$item/VERSION")"
      commit_msg+=" - $item v$item_version"
    done
    git -C "$PROJECT_ROOT" commit -m "$commit_msg" -m "Co-authored-by: Copilot <223556219+Copilot@users.noreply.github.com>"
    RELEASE_COMMITTED=1
    for item in "${components[@]}"; do
      item_version="$(tr -d '\r\n' < "$PROJECT_ROOT/$item/VERSION")"
      git -C "$PROJECT_ROOT" tag -a "$item-v$item_version" -m "$item Beta v$item_version"
    done
    return 0
  fi

  case "$component" in
    both)
      agent_ver="$(tr -d '\r\n' < "$PROJECT_ROOT/agent/VERSION")"
      server_ver="$(tr -d '\r\n' < "$PROJECT_ROOT/server/VERSION")"
      git -C "$PROJECT_ROOT" add agent/VERSION server/VERSION
      if [[ -n "$MESSAGE" ]]; then
        commit_msg="$MESSAGE - agent v$agent_ver, server v$server_ver"
      else
        commit_msg="chore: Release agent v$agent_ver, server v$server_ver"
      fi
      git -C "$PROJECT_ROOT" commit -m "$commit_msg" -m "Co-authored-by: Copilot <223556219+Copilot@users.noreply.github.com>"
      RELEASE_COMMITTED=1
      commit_sha="$(git -C "$PROJECT_ROOT" rev-parse --verify HEAD)"
      git -C "$PROJECT_ROOT" tag -a "agent-v$agent_ver" "$commit_sha" -m "Agent Release v$agent_ver"
      git -C "$PROJECT_ROOT" tag -a "server-v$server_ver" "$commit_sha" -m "Server Release v$server_ver"
      git -C "$PROJECT_ROOT" tag -f -a "latest-agent" "$commit_sha" -m "Latest Agent Release (v$agent_ver)"
      git -C "$PROJECT_ROOT" tag -f -a "latest-server" "$commit_sha" -m "Latest Server Release (v$server_ver)"
      if [[ "$agent_ver" =~ ^([0-9]+)\.([0-9]+)\.([0-9]+)$ ]]; then
        git -C "$PROJECT_ROOT" tag -f -a "agent-v${BASH_REMATCH[1]}" "$commit_sha" -m "Latest Agent v${BASH_REMATCH[1]} (v$agent_ver)"
        git -C "$PROJECT_ROOT" tag -f -a "agent-v${BASH_REMATCH[1]}.${BASH_REMATCH[2]}" "$commit_sha" -m "Latest Agent v${BASH_REMATCH[1]}.${BASH_REMATCH[2]} (v$agent_ver)"
      fi
      if [[ "$server_ver" =~ ^([0-9]+)\.([0-9]+)\.([0-9]+)$ ]]; then
        git -C "$PROJECT_ROOT" tag -f -a "server-v${BASH_REMATCH[1]}" "$commit_sha" -m "Latest Server v${BASH_REMATCH[1]} (v$server_ver)"
        git -C "$PROJECT_ROOT" tag -f -a "server-v${BASH_REMATCH[1]}.${BASH_REMATCH[2]}" "$commit_sha" -m "Latest Server v${BASH_REMATCH[1]}.${BASH_REMATCH[2]} (v$server_ver)"
      fi
      ;;
    server)
      git -C "$PROJECT_ROOT" add server/VERSION
      if [[ -n "$MESSAGE" ]]; then
        commit_msg="$MESSAGE - server v$version"
      else
        commit_msg="chore: Release server v$version"
      fi
      git -C "$PROJECT_ROOT" commit -m "$commit_msg" -m "Co-authored-by: Copilot <223556219+Copilot@users.noreply.github.com>"
      RELEASE_COMMITTED=1
      commit_sha="$(git -C "$PROJECT_ROOT" rev-parse --verify HEAD)"
      git -C "$PROJECT_ROOT" tag -a "server-v$version" -m "Server Release v$version"
      git -C "$PROJECT_ROOT" tag -f -a "latest-server" "$commit_sha" -m "Latest Server Release (v$version)"
      if [[ "$version" =~ ^([0-9]+)\.([0-9]+)\.([0-9]+)$ ]]; then
        git -C "$PROJECT_ROOT" tag -f -a "server-v${BASH_REMATCH[1]}" "$commit_sha" -m "Latest Server v${BASH_REMATCH[1]} (v$version)"
        git -C "$PROJECT_ROOT" tag -f -a "server-v${BASH_REMATCH[1]}.${BASH_REMATCH[2]}" "$commit_sha" -m "Latest Server v${BASH_REMATCH[1]}.${BASH_REMATCH[2]} (v$version)"
      fi
      ;;
    agent)
      git -C "$PROJECT_ROOT" add agent/VERSION
      if [[ -n "$MESSAGE" ]]; then
        commit_msg="$MESSAGE - agent v$version"
      else
        commit_msg="chore: Release agent v$version"
      fi
      git -C "$PROJECT_ROOT" commit -m "$commit_msg" -m "Co-authored-by: Copilot <223556219+Copilot@users.noreply.github.com>"
      RELEASE_COMMITTED=1
      commit_sha="$(git -C "$PROJECT_ROOT" rev-parse --verify HEAD)"
      git -C "$PROJECT_ROOT" tag -a "agent-v$version" -m "Agent Release v$version"
      git -C "$PROJECT_ROOT" tag -f -a "latest-agent" "$commit_sha" -m "Latest Agent Release (v$version)"
      if [[ "$version" =~ ^([0-9]+)\.([0-9]+)\.([0-9]+)$ ]]; then
        git -C "$PROJECT_ROOT" tag -f -a "agent-v${BASH_REMATCH[1]}" "$commit_sha" -m "Latest Agent v${BASH_REMATCH[1]} (v$version)"
        git -C "$PROJECT_ROOT" tag -f -a "agent-v${BASH_REMATCH[1]}.${BASH_REMATCH[2]}" "$commit_sha" -m "Latest Agent v${BASH_REMATCH[1]}.${BASH_REMATCH[2]} (v$version)"
      fi
      ;;
  esac
}

push_release() {
  local agent_ver server_ver version
  if [[ "$SKIP_PUSH" == "1" ]]; then
    status "Skipping push (--skip-push flag)" WARN
    return 0
  fi
  status "Pushing to GitHub..." STEP
  if [[ "$DRY_RUN" == "1" ]]; then
    status "[DRY RUN] Would push commits and tags" WARN
    return 0
  fi
  git -C "$PROJECT_ROOT" push
  if [[ "$final_version" == *-beta.* ]]; then
    local components=("$COMPONENT") item item_version
    [[ "$COMPONENT" != "both" ]] || components=(agent server)
    for item in "${components[@]}"; do
      item_version="$(tr -d '\r\n' < "$PROJECT_ROOT/$item/VERSION")"
      git -C "$PROJECT_ROOT" push origin "$item-v$item_version"
    done
    return 0
  fi
  case "$COMPONENT" in
    both)
      agent_ver="$(tr -d '\r\n' < "$PROJECT_ROOT/agent/VERSION")"
      server_ver="$(tr -d '\r\n' < "$PROJECT_ROOT/server/VERSION")"
      git -C "$PROJECT_ROOT" push origin "agent-v$agent_ver"
      git -C "$PROJECT_ROOT" push origin "server-v$server_ver"
      git -C "$PROJECT_ROOT" push -f origin latest-agent || status "Warning: Failed to push latest-agent tag" WARN
      git -C "$PROJECT_ROOT" push -f origin latest-server || status "Warning: Failed to push latest-server tag" WARN
      if [[ "$agent_ver" =~ ^([0-9]+)\.([0-9]+)\.([0-9]+)$ ]]; then
        git -C "$PROJECT_ROOT" push -f origin "agent-v${BASH_REMATCH[1]}" || true
        git -C "$PROJECT_ROOT" push -f origin "agent-v${BASH_REMATCH[1]}.${BASH_REMATCH[2]}" || true
      fi
      if [[ "$server_ver" =~ ^([0-9]+)\.([0-9]+)\.([0-9]+)$ ]]; then
        git -C "$PROJECT_ROOT" push -f origin "server-v${BASH_REMATCH[1]}" || true
        git -C "$PROJECT_ROOT" push -f origin "server-v${BASH_REMATCH[1]}.${BASH_REMATCH[2]}" || true
      fi
      ;;
    agent)
      version="$(tr -d '\r\n' < "$PROJECT_ROOT/agent/VERSION")"
      git -C "$PROJECT_ROOT" push origin "agent-v$version"
      git -C "$PROJECT_ROOT" push -f origin latest-agent || status "Warning: Failed to push latest-agent tag" WARN
      if [[ "$version" =~ ^([0-9]+)\.([0-9]+)\.([0-9]+)$ ]]; then
        git -C "$PROJECT_ROOT" push -f origin "agent-v${BASH_REMATCH[1]}" || true
        git -C "$PROJECT_ROOT" push -f origin "agent-v${BASH_REMATCH[1]}.${BASH_REMATCH[2]}" || true
      fi
      ;;
    server)
      version="$(tr -d '\r\n' < "$PROJECT_ROOT/server/VERSION")"
      git -C "$PROJECT_ROOT" push origin "server-v$version"
      git -C "$PROJECT_ROOT" push -f origin latest-server || status "Warning: Failed to push latest-server tag" WARN
      if [[ "$version" =~ ^([0-9]+)\.([0-9]+)\.([0-9]+)$ ]]; then
        git -C "$PROJECT_ROOT" push -f origin "server-v${BASH_REMATCH[1]}" || true
        git -C "$PROJECT_ROOT" push -f origin "server-v${BASH_REMATCH[1]}.${BASH_REMATCH[2]}" || true
      fi
      ;;
  esac
}

create_github_release() {
  local gh_available
  [[ "$CREATE_GITHUB_RELEASE" == "1" ]] || return 0
  if [[ "$DRY_RUN" == "1" ]]; then
    status "[DRY RUN] Would create GitHub release" WARN
    return 0
  fi
  gh_available="$(command -v gh || true)"
  if [[ -z "$gh_available" ]]; then
    status "GitHub CLI (gh) not found - skipping release creation" WARN
    return 0
  fi
  case "$COMPONENT" in
    both)
      status "Creating GitHub Release..." INFO
      create_github_release_component agent "$(tr -d '\r\n' < "$PROJECT_ROOT/agent/VERSION")"
      create_github_release_component server "$(tr -d '\r\n' < "$PROJECT_ROOT/server/VERSION")"
      ;;
    agent|server)
      create_github_release_component "$COMPONENT" "$(tr -d '\r\n' < "$PROJECT_ROOT/$COMPONENT/VERSION")"
      ;;
  esac
}

create_github_release_component() {
  local component="$1" version="$2" tag title changelog release_notes
  tag="${component}-v$version"
  title="${component^} v$version"
  if [[ "$COMPONENT" == "both" ]]; then
    if [[ "$component" == "agent" ]]; then changelog="$agent_changelog"; else changelog="$server_changelog"; fi
  else
    changelog="$changelog_content"
  fi
  release_notes=$(cat <<EOF
## PrintMaster ${component^} v$version

$changelog

---

### Installation

Docker:
docker pull ghcr.io/printmaster-org/printmaster-${component}:$version
EOF
)
  if [[ "$version" == *-beta.* ]]; then
    gh release create "$tag" --title "$title" --notes "$release_notes" --prerelease --latest=false
  else
    gh release create "$tag" --title "$title" --notes "$release_notes" --latest
  fi
}

main() {
  printf '\n╔══════════════════════════════════════════════════════╗\n'
  printf '║           PrintMaster Release Automation             ║\n'
  printf '╚══════════════════════════════════════════════════════╝\n\n'

  status "Component: $COMPONENT" INFO
  status "Bump Type: $BUMP_TYPE" INFO
  status "Dry Run: $DRY_RUN" INFO
  printf '\n'

  trap on_error ERR

  status "Running pre-flight checks..." STEP
  git -C "$PROJECT_ROOT" rev-parse --is-inside-work-tree >/dev/null
  if ! git_clean; then
    status "Uncommitted changes detected:" WARN
    git_status | sed 's/^/  /'
    status "Commit or stash changes before releasing" ERROR
    exit 1
  else
    status "Working directory is clean" INFO
  fi
  current_branch="$(git -C "$PROJECT_ROOT" rev-parse --abbrev-ref HEAD)"
  if [[ "$current_branch" != "main" && "$current_branch" != "master" ]]; then
    status "Currently on branch: $current_branch" WARN
    read -r -p $'Not on main branch. Continue? (y/N) ' continue_answer
    [[ "$continue_answer" == "y" ]] || exit 1
  fi

  status "Bumping version ($BUMP_TYPE)..." STEP
  case "$COMPONENT" in
    both)
      agent_result="$(update_version "$PROJECT_ROOT/agent/VERSION" "$BUMP_TYPE")"
      VERSIONS_CHANGED=1
      server_result="$(update_version "$PROJECT_ROOT/server/VERSION" "$BUMP_TYPE")"
      readarray -t agent_versions <<< "$agent_result"
      readarray -t server_versions <<< "$server_result"
      status "Agent: ${agent_versions[0]} → ${agent_versions[1]}" INFO
      status "Server: ${server_versions[0]} → ${server_versions[1]}" INFO
      final_version="${agent_versions[1]}"
      ;;
    server)
      version_result="$(update_version "$PROJECT_ROOT/server/VERSION" "$BUMP_TYPE")"
      readarray -t version_info <<< "$version_result"
      VERSIONS_CHANGED=1
      status "Server: ${version_info[0]} → ${version_info[1]}" INFO
      final_version="${version_info[1]}"
      ;;
    agent)
      version_result="$(update_version "$PROJECT_ROOT/agent/VERSION" "$BUMP_TYPE")"
      readarray -t version_info <<< "$version_result"
      VERSIONS_CHANGED=1
      status "Agent: ${version_info[0]} → ${version_info[1]}" INFO
      final_version="${version_info[1]}"
      ;;
  esac

  if [[ "$COMPONENT" == "both" ]]; then
    invoke_tests agent
    invoke_tests server
  else
    invoke_tests "$COMPONENT"
  fi

  if [[ "$COMPONENT" == "both" ]]; then
    build_release_binary agent
    build_release_binary server
  else
    build_release_binary "$COMPONENT"
  fi

  status "Generating release notes..." STEP
  if [[ "$COMPONENT" == "both" ]]; then
    agent_changelog="$(get_changelog_since_last_tag agent)"
    server_changelog="$(get_changelog_since_last_tag server)"
  else
    changelog="$(get_changelog_since_last_tag "$COMPONENT")"
  fi

  if [[ "$FAIL_ON_EMPTY_CHANGELOG" == "1" ]]; then
    if [[ "$COMPONENT" == "both" ]]; then
      is_changelog_meaningful "$agent_changelog" || { [[ "$DRY_RUN" == "1" ]] && status "[DRY RUN] Agent changelog would be empty" WARN || { status "Agent changelog is empty" ERROR; return 1; }; }
      is_changelog_meaningful "$server_changelog" || { [[ "$DRY_RUN" == "1" ]] && status "[DRY RUN] Server changelog would be empty" WARN || { status "Server changelog is empty" ERROR; return 1; }; }
    else
      is_changelog_meaningful "$changelog" || { [[ "$DRY_RUN" == "1" ]] && status "[DRY RUN] Changelog would be empty" WARN || { status "Changelog is empty" ERROR; return 1; }; }
    fi
  else
    status "FailOnEmptyChangelog disabled - continuing even if changelog is empty" WARN
  fi

  save_commit_and_tag "$COMPONENT" "$final_version"
  push_release
  changelog_content="${changelog:-}"
  create_github_release

  printf '\n╔══════════════════════════════════════════════════════╗\n'
  printf '║                  Release Complete!                   ║\n'
  printf '╚══════════════════════════════════════════════════════╝\n\n'

  if [[ "$COMPONENT" == "both" ]]; then
    status "Agent Version: ${agent_versions[1]}" INFO
    status "Server Version: ${server_versions[1]}" INFO
  else
    status "Version: $final_version" INFO
  fi
  status "Component: $COMPONENT" INFO
}

main "$@"
