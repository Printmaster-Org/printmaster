# Git & GitHub Integration - Complete ✅

## What Was Added

### 1. Automated Release Script (`release.ps1`)
**Location**: `c:\temp\printmaster\release.ps1`

**Usage**:
```powershell
# Patch release (bug fixes)
.\release.ps1 agent patch

# Minor release (new features)
.\release.ps1 agent minor

# Major release (breaking changes)
.\release.ps1 agent major

# Release server or both
.\release.ps1 server patch
.\release.ps1 both patch
```

**What it does automatically**:
1. ✅ Checks if working directory is clean
2. ✅ Bumps version in VERSION file(s)
3. ✅ Runs all tests
4. ✅ Builds optimized release binary
5. ✅ Commits VERSION change
6. ✅ Creates git tag (e.g., v0.2.0)
7. ✅ Pushes to GitHub

**Flags**:
- `--DryRun` - Preview what would happen
- `--SkipTests` - Skip test execution
- `--SkipPush` - Don't push to GitHub

---

### 2. Enhanced VS Code Tasks (`tasks.json`)

**Access**: Press `Ctrl+Shift+B` or Terminal → Run Task

**Build Tasks**:
- Build: Agent (Dev) ← **Default task**
- Build: Server (Dev)
- Build: Both (Dev)
- Build: Clean artifacts

**Test Tasks**:
- Test: Agent (all)
- Test: Server (all)

**Release Tasks**:
- Release: Agent Patch/Minor/Major
- Release: Server Patch
- Release: Both Patch

**Git Tasks**:
- Git: Status
- Git: Commit All
- Git: Push
- Git: Pull

**Utility Tasks**:
- Kill: PrintMaster processes
- Show Build Log
- Show Version

---

### 3. Enhanced Debug Configurations (`launch.json`)

**Access**: Press `F5` or Run and Debug panel

**Agent Debugging**:
- Debug: Agent (Default Port) - Port 8080
- Debug: Agent (Port 9090)
- Debug: Agent (Custom Config)
- Run: Agent (No Debug)

**Server Debugging**:
- Debug: Server (Default Port) - Port 3000
- Debug: Server (Port 8080)
- Run: Server (No Debug)

**Test Debugging**:
- Debug: Current Test Function
- Debug: All Tests in Package
- Debug: Current File Tests

**Compound**:
- Debug: Agent + Server Together ← Launches both simultaneously

---

### 4. Comprehensive Documentation (`BUILD_WORKFLOW.md`)

**Location**: `c:\temp\printmaster\docs\BUILD_WORKFLOW.md`

**Contents**:
- Quick reference for daily development
- Release procedures
- Semantic versioning guide (when to use patch/minor/major)
- VS Code integration examples
- Troubleshooting tips
- Best practices

---

### 5. Updated `.gitignore`

Now includes `.vscode/` tasks and launch configs for team consistency:
```gitignore
.vscode/*
!.vscode/tasks.json
!.vscode/launch.json
!.vscode/extensions.json
```

This ensures everyone on the team has the same build/debug experience.

---

## Quick Start

### Daily Development
```powershell
# Build and test
.\build.ps1 agent
.\build.ps1 test-all

# Or use VS Code: Ctrl+Shift+B → "Build: Agent (Dev)"
```

### Making a Release
```powershell
# For bug fixes (0.1.0 → 0.1.1)
.\release.ps1 agent patch

# For new features (0.1.0 → 0.2.0)
.\release.ps1 agent minor

# Or use VS Code: Terminal → Run Task → "Release: Agent Patch"
```

### Debugging
```
Press F5 → Select "Debug: Agent (Default Port)"
```

---

## Git Commands Still Work

All standard git commands work as usual:
```powershell
git status
git add -A
git commit -m "message"
git push
git pull
```

The automation is **additive** - it doesn't replace git, it just makes releases easier!

---

## Verification

Test the release script with a dry run:
```powershell
.\release.ps1 agent patch -DryRun
```

This shows you exactly what would happen without actually doing it.

---

## What's Tracked in Git

✅ **Committed**:
- Source code
- Documentation
- Build scripts
- VS Code tasks/launch configs
- VERSION files

❌ **Ignored**:
- Binaries (*.exe)
- Debug binaries (__debug_bin.exe)
- Logs (logs/)
- Databases (*.db)
- Config files (config.ini, keeps .example)

---

## GitHub Repository

**URL**: https://github.com/printmaster-org/printmaster
**Visibility**: Private (will go public at v0.9.0)
**Current Version**: v0.1.0

---

## Next Steps

1. **Test the release script**:
   ```powershell
   .\release.ps1 agent patch -DryRun
   ```

2. **Try VS Code tasks**:
   - Press `Ctrl+Shift+B`
   - Select "Build: Agent (Dev)"

3. **Try debugging**:
   - Press `F5`
   - Select "Debug: Agent (Default Port)"

4. **When ready for first real release**:
   ```powershell
   .\release.ps1 agent patch
   ```

---

## Documentation

See `docs/BUILD_WORKFLOW.md` for complete workflow guide.
