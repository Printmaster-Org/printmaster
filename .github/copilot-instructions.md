# Copilot Instructions for PrintMaster

> **Important**: Fix root causes, not symptoms. Do NOT write code "bandaids" or patches—solve the underlying problem.

> **Docs site is mandatory**: Any change in this repo that affects behavior, configuration, APIs, vendor/SNMP mappings, or security must also update the relevant pages in the **docs.printmaster.work** repo (sibling checkout `../docs.printmaster.work`). The legacy documentation trees have been removed; do not recreate them. See the section below.

> **Commit format is mandatory**: Use Conventional Commits (`type(scope): summary`) as specified in [AGENTS.md](../AGENTS.md#commit-messages-required). The `.githooks/commit-msg` hook enforces it; never use `--no-verify`.

## Required: Keep Code and Documentation in Tandem

- Every feature addition, behavior change, fix, removal, or deprecation requires a documentation-impact review before completion. Update affected docs in the same functional slice; do not defer documentation to a later cleanup.
- The canonical user/developer documentation and published API references live in **Printmaster-Org/docs.printmaster.work**, normally at the sibling checkout `../docs.printmaster.work` relative to this repo (e.g. `printmaster-org/printmaster` ↔ `printmaster-org/docs.printmaster.work`). Its `content/` guides and `static/openapi/` contracts are the editorial targets. Keep concise source-local READMEs, examples, contribution/security entry points, and configuration samples accurate where they remain relevant; do not duplicate full guides in this repo.
- Before editing code, locate affected guides, examples, and API contracts. Verify documentation against actual defaults, route registrations, handlers, authorization middleware, configuration structs, CLI flags, and serialization—not old READMEs or planned behavior.
- For API-surface changes, update the correct reference: Server user-session APIs (`static/openapi/server.yaml`), Agent-local APIs (`static/openapi/agent.yaml`), or Agent↔Server machine/protocol guide (`content/api/protocol.md`). These have different credentials and must not be conflated. If an operation is outside the reviewed OpenAPI subset, explicitly review/document its scope rather than implying the entire API is covered.
- Review method/path, parameters, request/response schemas, statuses/content types, pagination/filtering, authentication, role/tenant scope, limits, and compatibility. Changes to these semantics require docs/contract updates even if the URL is unchanged. Update contract coverage tests, examples, `x-source`, and the reviewed source revision for actually reviewed operations. Never imply uniform authorization or implemented features that the code does not provide.
- Contract `info.version`, product release versions, HTTP route versions, and the Agent↔Server protocol version are independent. Do not bump one solely because another changes. Explain compatibility impact; breaking HTTP or machine changes need a deliberate migration/versioning plan. Do not manually edit product VERSION files.
- Validate code with relevant tests, including negative authorization/tenant cases for API changes. In the docs checkout run `npm ci --ignore-scripts`, `npm test`, and a fresh Hugo/Docker build plus generated-site link/anchor checks (see its README and API_MAINTENANCE guide). Static OpenAPI validation is not proof of runtime authorization or schema conformance.
- Coordinate program and docs commits as a linked pair and summarize both commits, updated pages/contracts, and validation. If a change truly has no documentation impact, state why. If the docs checkout is unavailable, report the blocker and exact required updates; do not claim the slice fully complete or silently leave docs stale.

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
# CI starts three Playwright shards independently (two workers each), Jest,
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
