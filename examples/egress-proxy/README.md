# Browser egress allowlist with mitmproxy

`allowlist.py` is an optional reference proxy policy for browser traffic. It
complements Scout's page-level `AllowedOrigins` checks by checking cleartext
HTTP requests and HTTPS/WSS `CONNECT` authorities before opening a tunnel. It
does not decrypt TLS.

Use the same origin set in both places. Scout checks page-target requests and
navigation; the proxy covers browser traffic that page interception does not
see, including service-worker fetches and WebSocket handshakes. A mismatch can
make the two layers disagree, so keep the values together in your deployment
configuration.

## Run locally

Install the pinned proxy version used by the integration workflow:

```sh
python3 -m pip install 'mitmproxy==12.3.1'
```

In one terminal, start the proxy with the policy origins. The addon accepts
comma-separated `http://` and `https://` origins with no credentials, path,
query, or fragment:

```sh
cd examples/egress-proxy
export SCOUT_ALLOWED_ORIGINS='https://example.com,https://api.example.com:8443'
export SCOUT_ALLOW_PRIVATE_IPS=false
mitmdump \
  --mode regular \
  --listen-host 127.0.0.1 \
  --listen-port 8080 \
  --set ignore_hosts=.* \
  -s ./allowlist.py
```

`ignore_hosts=.*` passes allowed TLS tunnels through without TLS
interception, so the browser does not need to trust mitmproxy's certificate
authority. The addon still checks each CONNECT authority before the tunnel is
opened. The proxy is bound to loopback here; do not expose it to an untrusted
network.

By default, before opening each upstream connection, the addon resolves the
destination, rejects it if any DNS answer is non-public, and pins a selected
public address for the connection while retaining the hostname for TLS SNI.
This closes the hostname-to-private-address DNS rebinding path within traffic
that reaches this proxy. Set `SCOUT_ALLOW_PRIVATE_IPS=true` only for trusted
local development targets; that opt-in permits private answers while still
pinning the selected address for that connection. An invalid value fails
startup with a redacted `SCOUT_EGRESS_STARTUP` error record.

In another terminal, configure Scout with the same origins and proxy address:

```go
session, err := agent.NewSession(agent.SessionConfig{
    Headless:        true,
    ProxyServer:     "http://127.0.0.1:8080",
    AllowedOrigins:  []string{"https://example.com", "https://api.example.com:8443"},
})
```

The same locally launched-browser routing is available to the CLI, stateful
CLI, MCP server, and chat UI through `SCOUT_PROXY_SERVER`. Configure both
policy layers: `SCOUT_ALLOWED_ORIGINS` is read by this mitmproxy addon, while
`SCOUT_MCP_ALLOWED_ORIGINS` is Scout's page-level policy. The repeated values
must be kept in sync by your deployment configuration:

```sh
SCOUT_ALLOWED_ORIGINS='https://example.com,https://api.example.com:8443' \
SCOUT_ALLOW_PRIVATE_IPS=false \
mitmdump --mode regular --listen-host 0.0.0.0 --listen-port 8080 \
  --set ignore_hosts=.* -s ./allowlist.py
```

Start Scout with a proxy address reachable from its network namespace. On the
same host network namespace as mitmproxy, loopback works; with separate
containers, use the service DNS name as described below:

```sh
SCOUT_PROXY_SERVER=http://127.0.0.1:8080 \
SCOUT_MCP_ALLOWED_ORIGINS='https://example.com,https://api.example.com:8443' \
scout mcp serve --profile=core
```

The `127.0.0.1` address is correct only when Scout and mitmproxy share a host
network namespace. In containers or Kubernetes, use the proxy service DNS name
and port, for example `http://scout-egress-proxy:8080`. Bind a cross-container
listener only to a private interface, require authenticated proxy access at the
network boundary, and firewall it to the Scout workload. Do not expose the
unauthenticated example listener to the public internet.

The page origin allowlist and proxy policy remain separate configuration. A
configured proxy is not proof of enforcement; `scout doctor` labels it
unverified and returns the remediation `verify_proxy_health_and_decisions_or_configure_remote_browser_egress`.
Check the proxy process health and correlate its decision records with a
bounded browser request before treating the route as operational. Scout cannot
prove that every browser channel used the proxy; direct sockets, bypass rules,
remote CDP, other protocols, and operating-system traffic remain outside that
evidence. This setting does not change a browser attached with
`SCOUT_REMOTE_CDP`.

The session's built-in checks provide an early page-level decision. The
mitmproxy process enforces the browser-level HTTP/HTTPS/WSS destination check.
`ProxyServer` only points a locally launched browser at the proxy; Scout does
not start, configure, or monitor the proxy process for you. If the proxy is
unavailable, proxied requests fail to connect. Scout disables Chromium's
implicit loopback proxy bypass for locally launched browsers so localhost
destinations also pass through the configured proxy, and disables non-proxied
WebRTC UDP so STUN/ICE uses UDP SOCKS proxying when available or TCP proxying.
The Chrome 157 destination-side fixture confirmed the restriction against a
local STUN sink. The same fixture confirms navigation fails and the independent
HTTP destination receives no request when the configured proxy is unavailable.
These checks cover those tested paths for locally launched browsers; remote CDP
hosts need their own browser/network policy. Verify any custom browser wrapper
or deployment does not add bypass rules for destinations in scope.

For locally launched browsers, Scout also configures Chromium to map browser
hostname lookups to `NOTFOUND`, except for the configured proxy hostname.
Proxied destination names therefore remain available to the proxy while
Chromium's host-resolver-based prefetch and lookups do not send them to the
local resolver. Chromium must still resolve a hostname used by the proxy URL
itself; use a trusted resolver for that endpoint. The rule does not constrain
DNS or network activity from other processes on the host, or components that
bypass Chromium's host resolver.

At startup the addon emits one `SCOUT_EGRESS_STARTUP` JSON record containing
only a status and the number of allowed origins. Missing or malformed policy
emits an error record with a stable reason and then aborts addon loading; it
never falls back to allow-all. If mitmproxy cannot load the addon, check its
startup log and process health before routing browser traffic.

Each policy evaluation emits a `SCOUT_EGRESS_DECISION` JSON log record with a
unique decision ID, timestamp, request or CONNECT event, normalized scheme,
host, port, method, allow/deny verdict, and reason. It intentionally omits URL
paths, query strings, headers, and credentials. The integration fixture checks
these records against separate destination servers for denied service-worker
HTTP and WebSocket traffic, and against the allowed origin. If the proxy cannot
emit a decision record, it returns 503 and blocks the request rather than
silently allowing traffic without an audit record. These records prove what
this proxy decided; they do not prove that all browser traffic reached it.

For remote CDP, configure the proxy in the remote browser's network
environment; Scout cannot apply `SCOUT_PROXY_SERVER` to an already-running
browser. Launch Chrome with its proxy configured to the proxy service, and
apply the browser workload's firewall or network policy so Chrome can reach
the proxy and required control-plane endpoints but cannot send direct traffic
to unapproved destinations. Configure Scout with `SCOUT_REMOTE_CDP` and
`SCOUT_MCP_ALLOWED_ORIGINS`; leave `SCOUT_PROXY_SERVER` unset. Scout connects
to Chrome over CDP and does not connect to or configure the proxy in this
setup. The MCP status and action receipts identify remote host policy as
`unverified` because Scout cannot attest the browser's routing or firewall.
Verify the remote deployment with destination-side allow and deny controls
before relying on it.

Example process layout (replace the placeholder values with your deployment's
addresses and secret handling):

```sh
# On the browser host/container. Ensure the browser process itself receives
# this proxy setting and the host firewall blocks direct destination egress.
SCOUT_ALLOWED_ORIGINS='https://example.com,https://api.example.com:8443' \
mitmdump --mode regular --listen-host 0.0.0.0 --listen-port 8080 \
  --set ignore_hosts=.* -s ./allowlist.py

# Launch Chrome with --proxy-server=http://scout-egress-proxy:8080. Configure
# the proxy address and firewall for the browser workload, not Scout. Then
# configure Scout in its own process/container to attach over CDP:
SCOUT_REMOTE_CDP='http://browser:9222' \
SCOUT_MCP_ALLOWED_ORIGINS='https://example.com,https://api.example.com:8443' \
scout mcp serve --profile=core
```

The example does not configure browser authentication, TLS for the proxy
listener, Kubernetes NetworkPolicy, or host firewall rules. Supply those in
the deployment platform; origin checks and proxy routing are not substitutes
for an authenticated, network-restricted control plane.

## Policy and limits

- Matching is exact by scheme, normalized hostname, and effective port. A
  subdomain or another port needs its own origin entry. `wss://` maps to the
  corresponding `https://` authority; `ws://` maps to `http://`.
- Cleartext proxy requests with malformed URLs or schemes outside HTTP, HTTPS,
  WS, and WSS are rejected. This only covers traffic that reaches this proxy;
  browser or operating-system traffic that bypasses it still needs network
  controls.
- Denial logs and 403 response bodies identify the normalized scheme, host,
  and explicit port only. URL paths, queries, and embedded credentials are not
  copied into denial messages.
- mitmproxy evaluates each cleartext HTTP request independently, including a
  redirect destination. HTTPS redirects remain inside the encrypted tunnel;
  the proxy can enforce the destination host and port only when the browser
  opens a new CONNECT tunnel. It cannot inspect the redirect response or URL
  path without TLS interception.
- `AllowedOrigins` is a page-level policy for navigation and
  Fetch-interceptable requests. It does not cover service-worker-owned
  requests or WebSocket handshakes. The proxy integration test
  [`../../egress_proxy_integration_test.go`](../../egress_proxy_integration_test.go)
  exercises those two bypass paths against destination servers.
- The proxy checks host and port, then resolves and pins the selected
  destination address before connecting. Any non-public answer blocks the
  connection by default, including mixed public/private answers. The explicit
  `SCOUT_ALLOW_PRIVATE_IPS=true` opt-in permits private answers but keeps
  address pinning enabled. This policy applies only to connections routed
  through this addon; direct sockets, browser bypass rules, remote CDP routing, other
  protocols, and operating-system traffic still require network or firewall
  controls. Do not treat this addon as an OS sandbox or complete SSRF defense.
- A startup record proves only that the addon parsed a non-empty policy and
  registered its allowlist. A request decision proves what the addon decided
  for a request that reached it. Neither record proves that another browser
  channel, custom wrapper, remote browser, or OS process used the proxy.
- HTTPS and WSS payloads are not inspected. This policy does not inspect paths,
  headers, credentials, response bodies, or application-level authorization.
- Scout's `AllowedOrigins` accepts URLs whose paths are ignored as origin
  metadata; the proxy configuration intentionally requires bare origins.
  Normalize the list to origins when sharing it between the two layers.

## Run the addon unit tests

```sh
python3 -m unittest discover -s . -p 'test_*.py'
```

To verify the real mitmproxy startup contract, run:

```sh
python3 check_startup.py --output /tmp/scout-egress-startup.json
```

The check requires the pinned mitmproxy executable. It verifies that a valid
policy starts listening and that empty or malformed policies exit before
accepting connections. The integration workflow runs this check automatically.

The Chrome integration test is part of the repository's `integration` Go test
suite and requires Chrome plus mitmproxy. It verifies that denied service
worker HTTP, redirect, subresource, iframe, new-tab, and WSS attempts do not
reach their destination servers, and that each denial has a matching
structured proxy decision. It also confirms an allowed WSS handshake reaches
its destination server. The allowed TLS fixture listens on dual-stack loopback,
and the proxy integration evidence confirms the selected IPv6 peer is pinned
and reached. A separate Chrome fixture allowlists a hostname that resolves to
loopback and verifies a private-peer denial with zero requests at the
destination server. The addon unit tests also verify that a cleartext
redirect destination is evaluated by the same exact-origin rule as any other
request.
