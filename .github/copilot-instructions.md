# Copilot Instructions for PrintMaster

> **Important**: Fix root causes, not symptoms. Do NOT write code "bandaids" or patches—solve the underlying problem.

> **Documentation impact check is mandatory; a docs edit is conditional**: Assess every change for effects on user/operator behavior or required action. Update **docs.printmaster.work** when its public guidance or technical contracts would otherwise be inaccurate or incomplete; internal changes do not automatically need a site edit. The legacy documentation trees have been removed; do not recreate them. See the section below.

> **Commit format is mandatory**: Use Conventional Commits (`type(scope): summary`) as specified in [AGENTS.md](../AGENTS.md#commit-messages-required). The `.githooks/commit-msg` hook enforces it; never use `--no-verify`.

## Required: Assess Documentation Impact

- Assess documentation impact for every implementation change before completion. This check is required; a public documentation edit is not. Update affected material in the same functional slice when a reader-facing fact, procedure, or required action changes.
- The canonical user/operator guidance and deliberately maintained technical references live in **Printmaster-Org/docs.printmaster.work**, normally at sibling checkout `../docs.printmaster.work`. Use its task-focused `content/` pages and public API contracts under `static/openapi/`. Keep concise source-local READMEs, examples, contribution/security entry points, test fixtures, and configuration samples accurate when their own instructions change; do not duplicate full guides here.
- Site content should help users or operators install, configure, use, troubleshoot, secure, deploy, upgrade, or integrate with PrintMaster. API/protocol and contributor/build references are appropriate when they serve a defined reader task. Do not use the public site for implementation diaries, commit-by-commit audit evidence, internal backlogs, or routine engineering progress; keep those in code, PRs, issues, or maintainer records.
- Update public docs when a change affects observable behavior, task steps/outcomes, supported platforms, configuration/defaults, permissions/security expectations, API/protocol guarantees, compatibility, releases, deployment, or recovery. Internal refactors, test-only changes, and build/CI maintenance normally need no site edit when reader-facing behavior and instructions remain unchanged. If impact is uncertain, inspect the nearest relevant page and implementation to decide.
- When docs are needed, verify them against implementation: defaults, route registrations, handlers, authorization middleware, configuration structs, CLI flags, and serialization—not stale READMEs or planned behavior. For API-surface changes, use the correct reference: Server user-session APIs (`static/openapi/server.yaml`), Agent-local APIs (`static/openapi/agent.yaml`), or the Agent↔Server machine/protocol guide (`content/api/protocol.md`). These use different credentials and must not be conflated. Do not imply the reviewed OpenAPI subsets cover the whole API.
- For a changed API contract, review method/path, parameters, request/response schemas, statuses/content types, pagination/filtering, authentication, role/tenant scope, limits, and compatibility. Update contract tests, examples, `x-source`, and reviewed source revision only for operations actually reviewed. Static schema validation does not prove runtime authorization or conformance.
- Contract `info.version`, product release versions, HTTP route versions, and Agent↔Server protocol version are independent. Do not bump one solely because another changes. Explain compatibility impact; breaking HTTP or machine changes need a deliberate migration/versioning plan. Do not manually edit product VERSION files.
- Run relevant program tests for code changes, including negative authorization/tenant cases for API changes. When public docs/contracts change, run the docs repository's relevant tests and build/link checks (see its README and `API_MAINTENANCE.md`); static OpenAPI validation is not proof of runtime behavior.
- State the docs disposition in the completion summary: pages/contracts updated, or a concise reason no public-doc change was needed. If an implementation change requires a paired docs change, coordinate the commits with a one-way SHA reference: the docs commit references the implementation commit, not vice versa. If the docs checkout is unavailable, identify exact required pages and report the blocker rather than silently leaving affected guidance stale.

## Architecture (Agent-Server Fleet Management)

```
Agent (site) ──WebSocket/HTTP──▶ Server (central) ◀── Agent (site)
   │                                   │
   └── SNMP/mDNS discovery             └── Aggregates multi-site data
```

- **Agent** (`agent/`): Single binary + embedded web UI, discovers printers via SNMP, stores in SQLite
- **Server** (`server/`): Central hub, WebSocket proxy to agents, fleet-wide UI
- **Common** (`common/`): Shared packages (logger, config, web assets via `//go:embed`)

### Key Data Flows

| Flow | Key Files | Notes |
|------|-----------|-------|
| Agent Identity | `agent/config.go::LoadOrGenerateAgentID()` | UUID persists to `{datadir}/agent_id`, stays stable when name changes |
| Scanner Runtime | `agent/scanner_runtime.go`, `agent/scanner/coordinator.go`, `agent/scanner/work.go` | Bounded per-IP work: reachability → validated identity → optional detail/metrics; staged commit after identity |
| Device vs Metrics | `agent/storage/sqlite.go`, `interface.go` | Separate tables; metrics use time-series tiering (raw→hourly→daily) |
| Agent↔Server Comms | `agent/upload_worker.go`, `server/websocket.go` | WebSocket at `/api/v1/agents/ws`, HTTP fallback at `/heartbeat` |

## Build & Test Commands

```powershell
.\build.ps1 agent          # Dev build agent
.\build.ps1 server         # Dev build server
.\build.ps1 both           # Build both
# Builds gate on lint/all Go modules/JS syntax/Jest; browsers are opt-in.
.\build.ps1 agent -AllTests          # Full nonprivileged gate
.\build.ps1 agent -TestArgs @('--suite','lint,go') # Explicit narrower gate
.\build.ps1 agent -SkipTests         # Explicit bypass, never qualification evidence

# VS Code tasks (Ctrl+Shift+B): "Build: Agent (Dev)", "Test: Agent (all)", etc.

# Run tests
.\tests.ps1                         # Go/lint/Jest/all Playwright projects
.\tests.ps1 --skip-playwright       # Explicitly omit browsers for a focused run
.\tests.ps1 --all                   # Add Docker/docs/native fixtures
# Unix/macOS: ./tests.sh with the same flags; --help lists prerequisites/options.
# Unix-only root fixtures: ./tests.sh --all --privileged (requires sudo -n).
# CI uses the same runner. Report omissions and test-level skips; a partial
# selection is not a complete release baseline. Live installers are never enabled.
# Release (NEVER edit VERSION files manually)
.\release.ps1 agent patch  # build -> tests --all gate BEFORE version changes/tags
# CI starts six Playwright shards independently (two workers each), Jest,
# PostgreSQL, Docker/native checks, and a discovered parallel Go-module matrix.
```

## Project Patterns

### Interface-Based DI (for mocking)
Key interfaces to know: `DeviceStore`, `SNMPClient`, `VendorModule`, `Logger`
```go
// agent/storage/interface.go
type DeviceStore interface {
    Get(ctx context.Context, serial string) (*Device, error)
    Upsert(ctx context.Context, device *Device) error
    // ...
}
```

### Vendor SNMP Modules
Add new vendor support in `agent/scanner/vendor/`:
```go
// Implement VendorModule interface
type VendorModule interface {
    Name() string
    Detect(sysObjectID, sysDescr, model string) bool
    BaseOIDs() []string
    MetricOIDs(caps *capabilities.DeviceCapabilities) []string
    Parse(pdus []gosnmp.SnmpPDU) map[string]interface{}
}
```
See `vendor/registry.go` for registration, existing vendors (HP, Kyocera, Epson) for examples.

### Embedded Web Assets
Both binaries embed their UIs via `//go:embed web`. Shared CSS/JS in `common/web/shared.go`.

## Conventions

- **Cross-platform**: Pure-Go SQLite (`modernc.org/sqlite`), no CGO in CI
- **Context propagation**: All network calls use `context.Context` for cancellation
- **Parallel tests**: Use `t.Parallel()` in table-driven tests; avoid shared mutable state
- **Small commits**: Land each functional slice when tests pass—don't accumulate large diffs

## Quick Reference

| Task | Location |
|------|----------|
| Add vendor OID mapping | `agent/scanner/vendor/` (implement `VendorModule`) |
| Modify DB schema | `agent/storage/sqlite.go::initSchema()` |
| Agent↔Server API changes | `agent/upload_worker.go`, `server/websocket.go` |
| Shared web components | `common/web/shared.go` |
| Design docs | [Project structure](https://docs.printmaster.work/development/project-structure/), [Build workflow](https://docs.printmaster.work/development/build-workflow/) |
