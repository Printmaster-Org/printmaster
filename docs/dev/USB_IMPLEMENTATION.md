# USB Printer Support

**Status**: ✅ Implemented (Windows only)

---

## Summary

USB printer support is implemented as an **IPP-USB HTTP proxy**, not the SNMP-over-USB
approach originally planned (see "Abandoned approach" below). The agent enumerates
USB printers that expose an embedded web UI over the IPP-USB interface class, then
tunnels HTTP requests to that web UI over the USB bulk endpoints — the same
approach used by [OpenPrinting/ipp-usb](https://github.com/OpenPrinting/ipp-usb).

Metrics (page counts, toner levels, etc.) are obtained by scraping the printer's
embedded web UI/XML endpoints, not by querying SNMP OIDs over USB.

**Platform support**: Windows only today. Non-Windows builds compile a no-op stub
(`usbproxy_handlers_other.go`, `usbproxy_support_other.go`) so USB support always
reports `"supported": false` on Linux/macOS.

## Architecture

```
┌─────────────────────────────────────────────────────────────┐
│ PrintMaster Agent (Windows)                                  │
│                                                               │
│ ┌───────────────────────────────────────────────────────┐   │
│ │ usbproxy.Manager (agent/usbproxy/manager.go)           │   │
│ │  • Scans USB (WinUSB) devices every ScanInterval        │   │
│ │  • Filters interfaces to IPP-USB / printer class        │   │
│ │  • Matches devices to Windows spooler port names        │   │
│ │  • Opens per-device proxy sessions on demand             │   │
│ └───────────────────────────────────────────────────────┘   │
│                          │                                    │
│                          ▼                                    │
│ ┌───────────────────────────────────────────────────────┐   │
│ │ USBTransport (http.RoundTripper over WinUSB bulk I/O)   │   │
│ └───────────────────────────────────────────────────────┘   │
│                          │                                    │
│                          ▼                                    │
│ ┌───────────────────────────────────────────────────────┐   │
│ │ metrics.Collector (agent/usbproxy/metrics/)              │   │
│ │  • Vendor-specific scrapers (HP, Epson, generic, ...)    │   │
│ │  • Probes known XML/HTML endpoints, parses page counts   │   │
│ │    and supply levels                                     │   │
│ └───────────────────────────────────────────────────────┘   │
└─────────────────────────────────────────────────────────────┘
                          │
                          ▼
                  USB Printer (embedded web UI)
```

## Key files

| Component | Location |
|---|---|
| Proxy manager (scan, sessions, HTTP round-tripping) | [agent/usbproxy/manager.go](../../agent/usbproxy/manager.go) |
| Shared types (`USBPrinter`, `USBTransport`, `Config`) | [agent/usbproxy/types.go](../../agent/usbproxy/types.go) |
| Windows enumeration (WinUSB, SetupDi APIs) | [agent/usbproxy/usb_windows.go](../../agent/usbproxy/usb_windows.go) |
| Non-Windows stub (`IsSupported() == false`) | `agent/usbproxy/usb_other.go` |
| Metrics scraping / vendor registry | [agent/usbproxy/metrics/registry.go](../../agent/usbproxy/metrics/registry.go) |
| HTTP API handlers (Windows) | [agent/usbproxy_handlers.go](../../agent/usbproxy_handlers.go) |
| HTTP API no-op stubs (non-Windows) | `agent/usbproxy_handlers_other.go` |
| Startup wiring | `InitUSBProxy(...)` call in `agent/main.go` |

## HTTP API

- `GET /api/usb-printers` – list discovered USB printers with IPP-USB capability
- `POST /api/usb-printers/scan` – trigger an immediate rescan
- `GET /api/usb-printers/status` – proxy manager status + active session count
- `GET /api/usb-printers/metrics/{serial}` – scrape metrics from a specific printer
- `GET /api/usb-printers/probe/{serial}` – probe all known endpoints for debugging

## Data model

Discovered USB printers are **not** merged into the main `devices` table the way
network-discovered printers are. They're tracked separately in-memory by the
`usbproxy.Manager` and surfaced through the API above; metrics are fetched on
demand rather than polled into the time-series metrics tables.

---

## Abandoned approach: pure-Go SNMP-over-USB (`gousbsnmp`)

An earlier plan proposed a pure-Go library (`gousbsnmp`) implementing IEEE 1284.4
SNMP-over-USB framing, so USB printers could be queried with the same SNMP OIDs
used for network printers. That library was never built out — it was a dead end
and is **not** part of the codebase. Do not resurrect `github.com/mstrhakr/gousbsnmp`
or IEEE 1284.4 framing references from old docs/notes; the IPP-USB proxy approach
above is the current and only supported implementation.
