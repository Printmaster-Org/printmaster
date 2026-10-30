# Contributing to PrintMaster

Thank you for your interest in contributing to PrintMaster! This document provides guidelines and information to help you get started.

## Table of Contents

- [Code of Conduct](#code-of-conduct)
- [Getting Started](#getting-started)
- [Development Setup](#development-setup)
- [Project Structure](#project-structure)
- [Making Changes](#making-changes)
- [Testing](#testing)
- [Submitting Changes](#submitting-changes)
- [Adding Printer Support](#adding-printer-support)

## Code of Conduct

Please be respectful and constructive in all interactions. We're building something useful together.

## Getting Started

1. **Fork the repository** on GitHub
2. **Clone your fork** locally
3. **Create a branch** for your changes

```bash
git clone https://github.com/YOUR_USERNAME/printmaster.git
cd printmaster
git checkout -b feature/your-feature-name
```

## Development Setup

### Prerequisites

- **Go matching the module files** (CI: 1.27.0) - [Download](https://go.dev/dl/)
- **Git** - For version control
- **PowerShell** (Windows) or Bash (Linux/macOS) - For build scripts
- **Node.js 26**, locked npm test dependencies, compatible **staticcheck**, and
  installed Playwright browsers - For the shared test runner (see `--help`)

### Building

```powershell
# Windows - Build agent
.\build.ps1 agent

# Windows - Build server
.\build.ps1 server

# Windows - Build both
.\build.ps1 both

# Builds run lint, all Go modules and JS syntax/Jest first; no browsers by default.
.\build.ps1 agent -AllTests
.\build.ps1 agent -TestArgs @('--suite', 'lint,go')
.\build.ps1 agent -SkipTests # Explicit bypass
```

Unix equivalents: `./build.sh agent --all-tests`, or
`./build.sh agent --test-arg --suite=lint,go`, or `--skip-tests`.
Custom test arguments replace the default selection; use the runner's flags
to include browsers (`--suite lint,go,js,playwright`), omit suites or limit modules.
Release scripts call the build validation gate with **all nonprivileged suites
before changing versions**, then build without repeating the successful gate.
Missing Docker/docs prerequisites fail release validation. Explicit release
`-TestArgs` / repeated `--test-arg` overrides and `-SkipTests` / `--skip-tests`
remain visible bypasses, not proof of full qualification.

### Running Tests

```powershell
# Core baseline: Go/lint/Jest/all browser projects
.\tests.ps1
# Focused iteration with browsers explicitly omitted
.\tests.ps1 --skip-playwright
# Optional Docker and sibling-docs validation
.\tests.ps1 --all
```

On Unix/macOS use `./tests.sh` with the same flags. Both wrappers and CI use
`scripts/run-tests.js`; `--suite`, `--module`, `--package`, `--dry-run` and
`--help` support focused runs. `--privileged` explicitly adds isolated Unix
root fixtures; it never enables live installers. Missing prerequisites fail
visibly, and omitted/test-level skipped checks are not qualification evidence.
The runner stops at the first failed command by default. Use `--keep-going`
to collect independent failures; Docker log capture and cleanup still run on failure.
See the repository's [test runner and E2E notes](tests/README.md).

### Quick Development Workflow

```powershell
# Windows - Kill existing processes, build, and launch
.\dev\launch.ps1
```

## Project Structure

```
printmaster/
├── agent/          # Agent binary - printer discovery & monitoring
│   ├── scanner/    # SNMP scanning pipeline
│   │   └── vendor/ # Vendor-specific OID profiles
│   ├── storage/    # SQLite device/metrics storage
│   └── web/        # Embedded web UI
├── server/         # Server binary - multi-agent management
│   ├── handlers/   # HTTP API handlers
│   ├── storage/    # Database layer
│   └── web/        # Embedded web UI
└── common/         # Shared packages (logger, config, etc.)
```

Key maintainer references:
- [Test runner and E2E fixtures](tests/README.md)
- [Test data conventions](tests/testdata/README.md)
- [Repository instructions](AGENTS.md)

Maintain user/operator guides and API references in the separate
[documentation repository](https://github.com/Printmaster-Org/docs.printmaster.work).
Keep implementation notes and developer procedures beside the code and tools
they describe; do not use the public docs site as an engineering log or backlog.

## Making Changes

### Code Style

- Follow standard Go conventions (`gofmt`, `go vet`)
- Use meaningful variable and function names
- Add comments for non-obvious logic
- Keep functions focused and reasonably sized

### Commit Messages

Use [Conventional Commits](https://www.conventionalcommits.org/). Enable the enforcing hook once per clone:

```
git config core.hooksPath .githooks
```

```
type(scope): short imperative summary

Longer explanation if needed. Explain the "why" not just the "what".
```

Types: `feat`, `fix`, `docs`, `test`, `refactor`, `perf`, `build`, `ci`, `chore`, `style`, `revert`, `ux` (append `!` for breaking changes). The scope is optional and lowercase. Keep subjects at 100 characters or fewer, with no trailing period. See [AGENTS.md](AGENTS.md#commit-messages-required) for body and docs-pairing conventions.

Examples:
- `feat(scanner): add Brother MFC series support`
- `fix(api): correct pagination on device list endpoint`
- `docs(snmp): update reference with new OIDs`

### Don't

- **Don't write bandaid fixes** - If there's a bug, fix the root cause
- **Don't edit VERSION files manually** - Use `.\release.ps1` for releases
- **Don't commit large uncommitted diffs** - Land changes incrementally

## Testing

### Running Tests

```bash
# Run a focused component suite from the repository root
./tests.sh --suite go --module agent
./tests.sh --suite go --module server

# Direct Go commands remain useful for individual-test diagnostics (inside a module)
go test -v -run TestFunctionName ./package/

# Run with race detection
go test -race ./...
```

### Qualifying the 0.32 Legacy Bridge

0.32.x retains the legacy manifest/download protocol for the first upgrade
from 0.30.x/0.31.x. Remove legacy updater support in 0.33.0 Stable. Modern
Agents must continue requiring TUF; a TUF error must never enable legacy
signature authorization or an unverified installation.

The required CI `legacy-updaters` jobs compile the updater and HTTP client
from pinned 0.30.5, 0.31.0, and 0.31.1 commits against the current Server.
For a local diagnostic, from `server/`:

```powershell
$env:GOWORK = 'off'
$env:PRINTMASTER_TEST_LEGACY_UPDATERS = '1'
go test . -run '^TestHistoricalLegacyUpdaterAgainstCurrentServer$' -count=1 -timeout=15m -v
```

This test extracts historical source into temporary directories and verifies
legacy discovery, resumed downloads, hash rejection, authentication, and
installation handoff. Its artifact is not executable; Windows helper launch
and process restart are stubbed. It does not qualify a real service restart,
MSI transaction, package repository, or database migration. It requires the
pinned Git history and access to historical Go module dependencies.

Before publishing 0.32.0 Stable:

- Require a successful full CI attempt for the exact release source SHA.
- Run `legacy-linux-migration.yml` and `legacy-windows-migration.yml` on
  disposable hosted runners for 0.30.5, 0.31.0, and 0.31.1. Preserve and review
  failed/timed-out installer evidence; do not retry an ambiguous transaction.
- Publish exact Stable MSI/DEB/RPM TUF identities in addition to the five
  binary targets. The current release inventory builder and publisher guard
  accept binary targets only; extend both and verify the actual packaged
  bytes, including any RPM transformations. Keep Beta binary-only.
- Document pre-existing legacy MSI/portable format ambiguity and the
  matching-MSI manual workaround. Do not copy historical artifact collisions
  or misrepresent installer migration tests as legacy automatic MSI updates.
- Use release scripts, not manual VERSION edits. Prepare the version commit
  without pushing its tag; push the commit, qualify its exact SHA, then
  publish the tag. Agent and Server releases are independent.
- Enable the Stable publisher gate only for the authorized release. Verify
  published GitHub assets, package repositories, and public TUF metadata
  agree on hashes, versions, platform, architecture, and format.
- Verify a pilot upgrade preserves identity, credentials, configuration,
  saved devices, service health, and heartbeat. Test a subsequent TUF update
  separately from the legacy first hop before broad rollout.

### Writing Tests

- Use table-driven tests where appropriate
- Use `t.Parallel()` for independent tests
- Mock external dependencies (SNMP, network, etc.)
- See existing tests for patterns, e.g., `agent/scanner/` tests

### Test Coverage

Before submitting:
1. Ensure all existing tests pass
2. Add tests for new functionality
3. Add tests for bug fixes (to prevent regression)

## Submitting Changes

### Pull Request Process

1. **Update your branch** with the latest main:
   ```bash
   git fetch origin
   git rebase origin/main
   ```

2. **Run tests** and ensure they pass

3. **Push your branch** and create a Pull Request

4. **Fill out the PR template** completely

5. **Respond to feedback** promptly

### PR Guidelines

- Keep PRs focused - one feature or fix per PR
- Include tests for new code
- Update documentation if needed
- Reference related issues (e.g., "Fixes #123")

## Adding Printer Support

One of the most valuable contributions is adding support for new printer models!

### Quick Guide

1. **Find the vendor file** in `agent/scanner/vendor/`
2. **Add OID mappings** for the printer's SNMP data
3. **Test with a real device** if possible
4. **Submit a PR** with the model info

### Getting SNMP Data

If you have access to the printer:

```bash
# Basic device info
snmpwalk -v2c -c public <printer-ip> 1.3.6.1.2.1.1

# Printer MIB
snmpwalk -v2c -c public <printer-ip> 1.3.6.1.2.1.43

# Full walk (large output)
snmpwalk -v2c -c public <printer-ip> 1.3.6.1
```

### Resources

- [Public Printer SNMP reference](https://docs.printmaster.work/api/snmp-reference/) - Standard MIB and administrator guidance
- [Vendor scanner modules](agent/scanner/vendor/README.md) - Source and test conventions
- [agent/scanner/vendor/](agent/scanner/vendor/) - Existing vendor profiles
- Open a [Printer Support Request](https://github.com/printmaster-org/printmaster/issues/new?template=printer_support.yml) if you need help

## Questions?

- Check [existing issues](https://github.com/printmaster-org/printmaster/issues)
- Open a [Question issue](https://github.com/printmaster-org/printmaster/issues/new?template=question.yml)
- Read the [documentation](https://docs.printmaster.work/)

Thank you for contributing! 🖨️
