# Security Policy

## Reporting a Vulnerability

If you discover a security vulnerability in Scout (scout), please report it responsibly.

**Do not open a public GitHub issue for security vulnerabilities.**

Instead, please email security concerns to the maintainer or use [GitHub Security Advisories](https://github.com/klarlabs-studio/scout/security/advisories/new).

## Security Model

Scout launches and controls a Chrome browser process. Be aware of these security considerations:

### URL Validation
- Navigation blocks non-http(s) schemes (no `file://`, `javascript:`, `data:`)
- Private/loopback IPs are blocked by default (opt-in via `WithAllowPrivateIPs`)

### MCP Server
- The `eval` tool (arbitrary JavaScript execution) is disabled by default
- Enable only with `SCOUT_ENABLE_EVAL=1` environment variable
- All tool inputs are validated via typed structs

### File Operations
- Screenshot/PDF write paths reject path traversal (`..`)
- Files are written with `0600` permissions
- Temp directories for recordings use OS-provided secure temp paths

### Browser Process
- Chrome is launched with security-hardening flags
- Stealth middleware patches automation detection markers
- WebSocket CDP connection is localhost-only by default

## Supported Versions

| Version | Supported |
|---------|-----------|
| Latest  | Yes       |

## Dependencies

We monitor dependencies via `nox scan` for known vulnerabilities. Run `make nox` to check locally.
