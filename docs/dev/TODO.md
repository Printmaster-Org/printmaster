# PrintMaster TODO

Consolidated pending features and improvements from across the codebase.

**Current Version**: Agent v0.23.6, Server v0.23.6

---

## 🔴 High Priority (Pre-1.0)

### USB Printer Support
Implemented via an IPP-USB HTTP proxy (Windows only) — see
[USB_IMPLEMENTATION.md](USB_IMPLEMENTATION.md). The earlier pure-Go SNMP-over-USB
plan (`gousbsnmp`) was abandoned and never built.

- [x] IPP-USB proxy + web-scraped metrics (Windows)
- [ ] Linux/macOS USB device enumeration + proxy support
- [ ] Same metrics as network printers (page counts, toner, supplies) - USB scraping is best-effort per vendor, not at parity yet
- [ ] USB printer configuration UI
- [ ] USB/network printer differentiation in main devices table (currently tracked separately)

### SNMPv3 Support
Security enhancement for enterprise deployments.

- [ ] SNMPv3 authentication (MD5, SHA)
- [ ] SNMPv3 privacy/encryption (DES, AES)
- [ ] Context engine ID support
- [ ] Credentials storage (encrypted at rest)
- [ ] Per-device SNMPv3 configuration
- [ ] UI for SNMPv3 credential management

### Installer Repackaging (Auto-Update Phase 3)
Enable fleet-customized installers.

- [ ] Build packager: unpack release → inject fleet config → repack
- [ ] Authenticated download endpoints (`/api/v1/installers/{fleet}/{platform}`)
- [ ] "Download installer" button in server UI
- [ ] Support for ZIP/TAR/MSI wrapper formats

---

## 🟡 Medium Priority

### Analytics & Metrics

#### Supply Predictions
- [ ] Toner depletion forecasting based on historical usage
- [ ] Drum/imaging unit lifecycle tracking
- [ ] Fuser lifecycle tracking
- [ ] "Days remaining" estimates per supply
- [ ] Prediction confidence intervals

#### Cost Tracking
- [ ] Per-page cost configuration (mono/color)
- [ ] Per-device cost overrides
- [ ] Monthly cost aggregation
- [ ] Cost-per-department (if location/tags supported)
- [ ] Cost trending reports

#### Utilization Analytics
- [ ] Capacity utilization (% of duty cycle used)
- [ ] Peak usage hours detection
- [ ] Idle time tracking
- [ ] Utilization score (0-100)

### Performance Optimization

#### Load Testing
- [ ] Test with 100+ agents reporting to server
- [ ] Test with 1000+ printers tracked
- [ ] Database performance under load
- [ ] Memory leak detection
- [ ] CPU profiling and optimization

#### Caching
- [ ] SNMP result cache (configurable TTL)
- [ ] Device info cache (LRU, 1000 entries)
- [ ] HTTP response caching where appropriate

### Security Hardening

#### TLS/HTTPS
- [ ] HTTPS enforced by default
- [ ] Auto-generate self-signed certs on first run
- [ ] Certificate renewal/rotation
- [ ] Support for custom certificates
- [ ] TLS 1.2+ only

#### Additional Security
- [ ] Two-factor auth (TOTP)
- [ ] API rate limiting middleware
- [ ] Encrypt SNMP v3 credentials at rest
- [ ] Database encryption option (SQLCipher)

---

## 🟢 Lower Priority (Post-1.0)

### Fleet Dashboard Metrics
- [ ] Total fleet page count trends
- [ ] Fleet-wide supply levels overview
- [ ] Geographic distribution map
- [ ] Alert summary dashboard
- [ ] Device health score aggregation

### Job Analytics
- [ ] Job size distribution
- [ ] Duplex vs simplex ratio
- [ ] Color vs mono ratio
- [ ] Peak job times
- [ ] User/department job breakdown (if print accounting available)

### Environmental Metrics
- [ ] Paper usage tracking (total sheets)
- [ ] Energy consumption estimation
- [ ] Carbon footprint calculation
- [ ] Sustainability score
- [ ] Environmental report generation

### Reporting
- [ ] PDF/Excel export
- [ ] Scheduled reports (email digest)
- [ ] Custom report builder
- [ ] Multi-site reports

### Integrations
- [ ] Webhook notifications (device discovery, alerts)
- [ ] MQTT publishing (IoT integration)
- [ ] Prometheus metrics endpoint
- [ ] Syslog forwarding

---

## 🔧 Technical Debt

### Documentation
- [ ] Complete API reference for new endpoints
- [ ] Document all environment variables
- [ ] Video walkthroughs (YouTube)
- [ ] Migration guide (0.x → 1.0)

### Testing
- [ ] Browser compatibility (Chrome, Firefox, Edge, Safari)
- [ ] Accessibility testing (web UI)
- [ ] Integration test coverage for WebSocket proxy
- [ ] Vendor-specific SNMP response mocking

### Code Quality
- [ ] Config validation for all settings
- [ ] Standardize error handling patterns
- [ ] Database index optimization audit

---

## ✅ Recently Completed

These items were completed and can be referenced in their implementation:

- ✅ Multi-agent server architecture (v0.2.0)
- ✅ WebSocket proxy for remote agent/device access
- ✅ Database rotation and recovery system
- ✅ Metrics tiering (raw/hourly/daily/monthly)
- ✅ Linux package repositories (APT/DNF)
- ✅ Docker multi-arch builds
- ✅ Paper tray status tracking (December 2025)
- ✅ Shared web assets via go:embed
- ✅ Auto-update policy framework (Phase 1-2)
- ✅ Release manifest signing (Ed25519)

---

## Notes

- USB support (IPP-USB proxy) works on Windows; Linux/macOS parity is the main gap
- Security features (SNMPv3, TOTP) needed before enterprise adoption
- Analytics features can ship incrementally post-1.0
- Avoid scope creep on reporting - MVP first

*Last Updated: December 2025*
