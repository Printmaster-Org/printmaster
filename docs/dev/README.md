# PrintMaster Developer Documentation

Technical documentation for PrintMaster development. For user documentation, see [/docs](../README.md).

**Current Version**: Agent v0.23.6, Server v0.23.6

---

## Start Here
- [TODO.md](TODO.md) – **Consolidated pending features and improvements**
- [PROJECT_STRUCTURE.md](PROJECT_STRUCTURE.md) – Repository layout and module overview
- [BUILD_WORKFLOW.md](BUILD_WORKFLOW.md) – Build/test/release commands + VS Code tasks

## User Documentation (Moved)
These docs are now in the parent [/docs](../README.md) folder:
- [API Reference](../api/README.md) – REST API for agent and server
- [Configuration](../CONFIGURATION.md) – Config files and environment variables
- [Docker Deployment](../deployment/docker.md) – Container deployment
- [Unraid Deployment](../deployment/unraid.md) – Unraid-specific setup

## Architecture & Internals
- [SECURITY_ARCHITECTURE.md](SECURITY_ARCHITECTURE.md) – Authentication/authorization design
- [WEBSOCKET_PROXY.md](WEBSOCKET_PROXY.md) – Server proxy tunnel details
- [SNMP_REFERENCE.md](SNMP_REFERENCE.md) – OIDs, vendor detection, discovery process
- [SNMP_RESEARCH_NOTES.md](SNMP_RESEARCH_NOTES.md) – Protocol research and notes
- [Printer-MIB.mib](Printer-MIB.mib) – Standard Printer-MIB file
- [RANGE_SYNTAX.md](RANGE_SYNTAX.md) – IP range syntax documentation
- [USB_IMPLEMENTATION.md](USB_IMPLEMENTATION.md) – USB printer support via IPP-USB proxy (Windows only)

## Development & Testing
- [TESTING.md](TESTING.md) – Testing strategy and patterns
- [TEST_COVERAGE_ANALYSIS.md](TEST_COVERAGE_ANALYSIS.md) – Coverage status and gaps

## Feature Plans (In Progress)
- [AUTO_UPDATE_PLAN.md](AUTO_UPDATE_PLAN.md) – Agent/server auto-update implementation
- [EPSON_REMOTE_MODE_PLAN.md](EPSON_REMOTE_MODE_PLAN.md) – Epson remote-mode integration

## Reference
- [DEPRECATIONS.md](DEPRECATIONS.md) – Removed features and migration notes
- [vendor/](vendor/) – Vendor-specific OID documentation

---

> For condensed AI/assistant guidance, see `.github/copilot-instructions.md`
