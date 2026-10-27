# Vendor scanner modules

Vendor modules add model-specific SNMP queries and parsers to the Agent scanner. The Go implementation and its regression fixtures are the source of truth for supported OIDs and parsing behavior.

## Add or change a mapping

1. Find the vendor's existing module in this directory and follow its query/parser pattern.
2. Add bounded OID queries and parse only values whose type, index, and units are understood. Keep generic Printer-MIB fallback behavior intact.
3. Add synthetic or sanitized fixtures and focused tests under the vendor module. Do not commit customer walks, device IPs, serial numbers, hostnames, communities, or credentials.
4. Run focused tests and the repository's required test gate. Update the public SNMP reference only when users need a new setup or support expectation; internal parser changes alone do not require a public docs edit.

Use vendor-published MIBs and authorized observations. Do not copy proprietary implementation code or include reverse-engineered vendor artifacts in this repository. A vendor-specific parser is not a guarantee that every model or firmware exposes the same data.
