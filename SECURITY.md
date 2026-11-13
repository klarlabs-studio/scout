# Security Policy

## Reporting a Vulnerability

If you discover a security vulnerability in Scout (scout), please report it responsibly.

**Do not open a public GitHub issue for security vulnerabilities.**

Instead, please email security concerns to the maintainer or use [GitHub Security Advisories](https://github.com/klarlabs-studio/scout/security/advisories/new).

## Security Model

Scout launches and controls a Chrome browser process. Be aware of these security considerations:

### URL Validation
- Navigation and page-target HTTP(S) requests, redirects, and subresources are checked against the URL policy. Service-worker requests and WebSocket handshakes are outside page-target Fetch interception.
- Non-http(s) navigation schemes are blocked. Private, loopback, and link-local IPs are blocked by default (opt-in via `WithAllowPrivateIPs`).

### MCP Server
- Arbitrary JavaScript `eval` is disabled by default in the chat UI and can be enabled only with `SCOUT_ENABLE_EVAL=1`. The one-shot `scout eval` CLI command is an explicit operator invocation.
- MCP output paths reject traversal and protected system/credential locations. Relative output paths resolve under the OS temp directory.
- MCP capture and profile outputs are created with private file permissions and refuse existing paths; profile files contain cookies and local storage.
- All tool inputs are validated via typed structs

### File Operations
- MCP screenshot, annotated screenshot, hybrid screenshot, PDF, page snapshot, profile, playbook, and trace outputs reject path traversal, protected system/credential locations, and existing destination files.
- Capture and profile files use `0600` permissions. Trace and generated recording directories use private temporary directories; explicit recording parents should be writable only by trusted local users.
- Upload tools block well-known credential files and directories.

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

The release workflow downloads `mcp-publisher` from a versioned registry
release and verifies the archive against its pinned SHA-256 before running it
with GitHub OIDC publishing credentials. Keep the publisher version and digest
updated together from the official
[registry release assets](https://github.com/modelcontextprotocol/registry/releases).
