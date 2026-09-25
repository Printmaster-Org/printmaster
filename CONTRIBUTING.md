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

- **Go 1.24+** - [Download](https://go.dev/dl/)
- **Git** - For version control
- **PowerShell** (Windows) or Bash (Linux/macOS) - For build scripts

### Building

```powershell
# Windows - Build agent
.\build.ps1 agent

# Windows - Build server
.\build.ps1 server

# Windows - Build both
.\build.ps1 both
```

### Running Tests

```powershell
# Test agent packages
cd agent; go test ./... -v

# Test server packages
cd server; go test ./... -v
```

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
├── common/         # Shared packages (logger, config, etc.)
└── docs/           # Design documentation
```

Key documentation:
- [docs/BUILD_WORKFLOW.md](docs/BUILD_WORKFLOW.md) - Build, test, and release procedures
- [docs/PROJECT_STRUCTURE.md](docs/PROJECT_STRUCTURE.md) - Detailed architecture overview
- [docs/ROADMAP.md](docs/ROADMAP.md) - Planned features and priorities

## Making Changes

### Code Style

- Follow standard Go conventions (`gofmt`, `go vet`)
- Use meaningful variable and function names
- Add comments for non-obvious logic
- Keep functions focused and reasonably sized

### Commit Messages

Write clear, descriptive commit messages:

```
component: short description of change

Longer explanation if needed. Explain the "why" not just the "what".
```

Examples:
- `agent/scanner: add Brother MFC series support`
- `server/api: fix pagination on device list endpoint`
- `docs: update SNMP reference with new OIDs`

### Don't

- **Don't write bandaid fixes** - If there's a bug, fix the root cause
- **Don't edit VERSION files manually** - Use `.\release.ps1` for releases
- **Don't commit large uncommitted diffs** - Land changes incrementally

## Testing

### Running Tests

```bash
# Run all tests for a component
cd agent && go test ./...
cd server && go test ./...

# Run specific test
go test -v -run TestFunctionName ./package/

# Run with race detection
go test -race ./...
```

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

- [docs/SNMP_REFERENCE.md](docs/SNMP_REFERENCE.md) - OID documentation
- [agent/scanner/vendor/](agent/scanner/vendor/) - Existing vendor profiles
- Open a [Printer Support Request](https://github.com/printmaster-org/printmaster/issues/new?template=printer_support.yml) if you need help

## Questions?

- Check [existing issues](https://github.com/printmaster-org/printmaster/issues)
- Open a [Question issue](https://github.com/printmaster-org/printmaster/issues/new?template=question.yml)
- Read the [documentation](docs/)

Thank you for contributing! 🖨️
