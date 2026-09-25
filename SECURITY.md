# Security Policy

## Supported Versions

We release security patches for the following versions:

| Component | Version | Supported          |
| --------- | ------- | ------------------ |
| Agent     | latest  | :white_check_mark: |
| Server    | latest  | :white_check_mark: |

We recommend always running the latest version for the best security.

## Reporting a Vulnerability

**Please do not report security vulnerabilities through public GitHub issues.**

Instead, please report them privately via one of these methods:

### Option 1: GitHub Security Advisories (Preferred)

1. Go to the [Security tab](https://github.com/printmaster-org/printmaster/security)
2. Click "Report a vulnerability"
3. Fill out the form with details

### Option 2: Email

Send details to the repository owner via GitHub (check profile for contact info).

### What to Include

Please include as much of the following information as possible:

- Type of vulnerability (e.g., SQL injection, XSS, authentication bypass)
- Affected component (Agent, Server, Web UI, API)
- Version(s) affected
- Step-by-step instructions to reproduce
- Proof-of-concept or exploit code (if available)
- Impact assessment
- Any suggested fixes

## Response Timeline

- **Initial Response**: Within 72 hours
- **Status Update**: Within 7 days
- **Fix Timeline**: Depends on severity
  - Critical: 1-7 days
  - High: 7-14 days
  - Medium: 14-30 days
  - Low: Next regular release

## Security Best Practices

When deploying PrintMaster:

### Network Security

- Run agents on isolated management VLANs when possible
- Use firewall rules to restrict agent-server communication
- Don't expose the server directly to the internet without a reverse proxy

### Authentication

- **Change the default admin password immediately**
- Use strong, unique passwords
- Enable TLS for agent-server communication in production

### Server Configuration

```toml
# Recommended: Enable TLS
[server]
tls_cert = "/path/to/cert.pem"
tls_key = "/path/to/key.pem"

# Use token authentication for agents
[agents]
require_token = true
```

### Docker Deployment

- Don't run containers as root when possible
- Use read-only file systems where feasible
- Keep images updated

### SNMP Security

- Use SNMPv2c with non-default community strings
- Consider SNMPv3 for sensitive environments (future feature)
- Restrict SNMP access at the printer level

## Known Security Considerations

### SNMP Community Strings

SNMP community strings are stored in the configuration file. Protect this file with appropriate permissions:

```bash
# Linux
chmod 600 /etc/printmaster/config.toml

# Or use environment variables
export SNMP_COMMUNITY="your-community-string"
```

### Web UI Sessions

- Sessions expire after inactivity
- Cookies are HTTP-only and secure (when using TLS)
- CSRF protection is enabled

### API Authentication

- Agent-to-server communication uses token authentication
- API endpoints require authentication
- Rate limiting is recommended via reverse proxy

## Security Updates

Security updates are announced via:

- GitHub Releases (tagged with security label when applicable)
- Release notes in CHANGELOG

Subscribe to releases to stay informed:
1. Click "Watch" on the repository
2. Select "Custom" → "Releases"

## Acknowledgments

We appreciate responsible disclosure. Contributors who report valid security issues will be acknowledged (unless they prefer to remain anonymous).

Thank you for helping keep PrintMaster secure! 🔐
