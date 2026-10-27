# Integration and End-to-End Tests

This directory contains cross-component tests and fixtures. From the repository
root, use the shared PowerShell or Bash wrapper; both wrappers and CI invoke
`scripts/run-tests.js`.

```powershell
.\tests.ps1                         # Core: lint, Go, Jest, and Playwright
.\tests.ps1 --skip-playwright       # Focused non-browser run
.\tests.ps1 --suite go --module agent --package ./storage
.\tests.ps1 --with-docker           # Add PostgreSQL and isolated Docker E2E
.\tests.ps1 --with-docs             # Add sibling docs tests/build/link checks
.\tests.ps1 --all                   # All nonprivileged suites
.\tests.ps1 --all --skip-docker     # Omit Docker and PostgreSQL
.\tests.ps1 --help
```

On Linux/macOS, use `./tests.sh` with the same options. `--suite` selects only
the named suites; `--module` limits Go/lint modules and `--package` limits Go
tests. Check `--help` for the current options and prerequisites. A partial suite
selection is not full CI qualification; report omitted suites and skips.

| Suite | Purpose |
| --- | --- |
| `lint` | `go vet` and compatible `staticcheck` across discovered Go modules |
| `go` | Go unit tests across discovered modules, including the mock E2E module |
| `js` | Web JavaScript syntax checks and Jest tests |
| `playwright` | Browser tests across the configured projects |
| `integration` | Tagged PostgreSQL storage tests |
| `docker` | Isolated Compose E2E using temporary data and scoped cleanup |
| `native` | Permission, descriptor-substitution, and native-helper regressions |
| `docs` | Sibling docs-site tests, Hugo build, and generated-site checks |
| `privileged` | Explicit Unix-only root fixtures; requires `--privileged` |

Install the toolchain and optional test prerequisites separately. The runner
does not install missing tools, start Docker Desktop, or run live installers.
Docker E2E uses an isolated Compose project and never adopts existing databases,
prunes Docker globally, or interrupts installed services. Its local SQLite
fixtures and cleanup behavior are covered by the tests.

For fixture formats and ownership, see [testdata/README.md](testdata/README.md).
User-facing installation and operation guidance lives at
[docs.printmaster.work](https://docs.printmaster.work/).
