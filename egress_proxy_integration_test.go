//go:build integration

package browse

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// This integration test proves the reference proxy blocks denied destinations
// across service workers, redirects, subresources, iframes, new targets, and
// WebSocket handshakes.
func TestIntegrationEgressProxyBlocksUnallowedTraffic(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome and mitmproxy")
	}
	mitmdump, err := exec.LookPath("mitmdump")
	if err != nil {
		t.Skip("mitmdump not installed")
	}
	chrome, err := findChromeForEgressTest()
	if err != nil {
		t.Skip("Chrome not installed")
	}

	var serviceWorkerControlHits atomic.Int32
	var disallowedHTTPHits atomic.Int32
	var disallowedRedirectHits atomic.Int32
	var disallowedResourceHits atomic.Int32
	var disallowedIframeHits atomic.Int32
	var disallowedNewTabHits atomic.Int32
	var disallowedWSHits atomic.Int32
	var allowedWSHits atomic.Int32
	var disallowedHTTPURL string
	var disallowedRedirectURL string
	var disallowedResourceURL string
	var disallowedIframeURL string
	allowedWSUpgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	allowed := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/socket":
			conn, err := allowedWSUpgrader.Upgrade(w, r, nil)
			if err != nil {
				return
			}
			allowedWSHits.Add(1)
			_ = conn.Close()
		case "/sw.js":
			w.Header().Set("Content-Type", "application/javascript")
			fmt.Fprintf(w, `self.addEventListener('install',e=>e.waitUntil(self.skipWaiting()));self.addEventListener('activate',e=>e.waitUntil(self.clients.claim()));self.addEventListener('fetch',e=>{if(new URL(e.request.url).pathname==='/trigger')e.respondWith((async()=>{await fetch('/sw-ran');try{return await fetch(%q)}catch(_){return new Response('blocked')}})())})`, disallowedHTTPURL)
		case "/sw-ran":
			serviceWorkerControlHits.Add(1)
			w.WriteHeader(http.StatusNoContent)
		case "/trigger":
			fmt.Fprint(w, "uncontrolled response")
		case "/redirect":
			http.Redirect(w, r, disallowedRedirectURL, http.StatusFound)
		default:
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprintf(w, `<!doctype html><body><img id="denied-resource" src=%q><iframe id="denied-iframe" src=%q></iframe><script>
if (new URL(location.href).searchParams.has('controlled')) {
  fetch('/trigger').then(r=>r.text()).then(v=>document.body.dataset.result=v);
  fetch('/redirect').then(r=>r.text()).then(v=>document.body.dataset.redirect=v).catch(()=>document.body.dataset.redirect='blocked');
} else {
  navigator.serviceWorker.register('/sw.js').then(async () => {
    await navigator.serviceWorker.ready;
    if (!navigator.serviceWorker.controller) await new Promise(resolve => navigator.serviceWorker.addEventListener('controllerchange', resolve, {once:true}));
    location.replace('/?controlled=1');
  });
}

</script></body>`, disallowedResourceURL, disallowedIframeURL)
		}
	}))
	allowedListener, err := net.Listen("tcp", "[::]:0")
	if err != nil {
		t.Fatalf("listen for dual-stack allowed fixture: %v", err)
	}
	allowed.Listener = allowedListener
	allowed.StartTLS()
	defer allowed.Close()

	disallowedHTTPServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		disallowedHTTPHits.Add(1)
		http.Error(w, "must be blocked by proxy", http.StatusBadRequest)
	}))
	defer disallowedHTTPServer.Close()
	disallowedRedirectServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		disallowedRedirectHits.Add(1)
		http.Error(w, "redirect destination must be blocked by proxy", http.StatusBadRequest)
	}))
	defer disallowedRedirectServer.Close()
	disallowedResourceServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		disallowedResourceHits.Add(1)
		http.Error(w, "subresource destination must be blocked by proxy", http.StatusBadRequest)
	}))
	defer disallowedResourceServer.Close()
	disallowedIframeServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		disallowedIframeHits.Add(1)
		http.Error(w, "iframe destination must be blocked by proxy", http.StatusBadRequest)
	}))
	defer disallowedIframeServer.Close()
	disallowedNewTabServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		disallowedNewTabHits.Add(1)
		http.Error(w, "new-tab destination must be blocked by proxy", http.StatusBadRequest)
	}))
	defer disallowedNewTabServer.Close()
	disallowedWSServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			disallowedWSHits.Add(1)
		}
		http.Error(w, "must be blocked by proxy", http.StatusBadRequest)
	}))
	defer disallowedWSServer.Close()

	allowedURL := replaceLoopbackHost(allowed.URL, "allowed.localhost")
	disallowedURL := replaceLoopbackHost(disallowedHTTPServer.URL, "denied.localhost")
	disallowedRedirectBaseURL := replaceLoopbackHost(disallowedRedirectServer.URL, "denied-redirect.localhost")
	disallowedRedirectURL = disallowedRedirectBaseURL + "/redirected"
	disallowedResourceURL = replaceLoopbackHost(disallowedResourceServer.URL, "denied-resource.localhost") + "/pixel.png"
	disallowedIframeURL = replaceLoopbackHost(disallowedIframeServer.URL, "denied-iframe.localhost") + "/frame"
	disallowedNewTabURL := replaceLoopbackHost(disallowedNewTabServer.URL, "denied-new-tab.localhost") + "/page"
	disallowedWSURL := replaceLoopbackHost(disallowedWSServer.URL, "denied-ws.localhost")
	disallowedHTTPURL = disallowedURL + "/private-data"
	allowedPort := portOf(t, allowedURL)
	proxyPort := freeTCPPort(t)
	var proxyLogs lockedBuffer
	proxy := exec.Command(mitmdump,
		"--mode", "regular",
		"--listen-host", "127.0.0.1",
		"--listen-port", fmt.Sprint(proxyPort),
		"--set", "ignore_hosts=.*",
		"-s", filepath.Join("examples", "egress-proxy", "allowlist.py"),
	)
	proxy.Env = append(os.Environ(), "SCOUT_ALLOWED_ORIGINS=https://allowed.localhost:"+fmt.Sprint(allowedPort), "SCOUT_ALLOW_PRIVATE_IPS=true")
	proxy.Stdout = &proxyLogs
	proxy.Stderr = &proxyLogs
	if err := proxy.Start(); err != nil {
		t.Fatalf("start mitmdump: %v", err)
	}
	defer stopProcess(proxy)
	waitForTCP(t, fmt.Sprintf("127.0.0.1:%d", proxyPort), 10*time.Second, &proxyLogs)

	chromePort := freeTCPPort(t)
	profile, err := os.MkdirTemp("", "scout-egress-chrome-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(profile)
	browser := exec.Command(chrome,
		"--headless=new", "--no-sandbox", "--disable-gpu",
		"--remote-debugging-address=127.0.0.1", "--remote-debugging-port="+fmt.Sprint(chromePort),
		"--remote-allow-origins=*", "--user-data-dir="+profile,
		"--proxy-server=http://127.0.0.1:"+fmt.Sprint(proxyPort),
		"--proxy-bypass-list=<-loopback>", "--ignore-certificate-errors", "--disable-quic",
		"about:blank",
	)
	browser.Stdout = os.Stdout
	browser.Stderr = os.Stderr
	if err := browser.Start(); err != nil {
		t.Fatalf("start Chrome: %v", err)
	}
	defer stopProcess(browser)
	cdpURL := waitForCDP(t, chromePort, 20*time.Second)

	engine := New(WithRemoteCDP(cdpURL), WithHeadless(true), WithAllowPrivateIPs(true))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := engine.LaunchContext(ctx); err != nil {
		t.Fatalf("attach Scout to Chrome: %v", err)
	}
	defer engine.Close()
	page, err := engine.NewPage()
	if err != nil {
		t.Fatalf("create page: %v", err)
	}
	defer page.Close()
	if err := page.NavigateContext(ctx, allowedURL); err != nil {
		t.Fatalf("navigate to allowed fixture: %v (proxy output: %s)", err, proxyLogs.String())
	}

	deadline := time.Now().Add(10 * time.Second)
	serviceWorkerBlocked := false
	for time.Now().Before(deadline) {
		value, evalErr := page.EvaluateContext(ctx, `document.body && document.body.dataset.result || ''`)
		if evalErr == nil && fmt.Sprint(value) == "blocked" {
			serviceWorkerBlocked = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if hits := serviceWorkerControlHits.Load(); hits == 0 {
		t.Fatalf("service-worker control request never reached allowed server; proxy setup inconclusive; logs: %s", proxyLogs.String())
	}
	if !serviceWorkerBlocked {
		t.Fatalf("service worker did not receive the expected blocked response; logs: %s", proxyLogs.String())
	}
	if hits := disallowedHTTPHits.Load(); hits != 0 {
		t.Fatalf("service-worker fetch escaped proxy %d times", hits)
	}
	if hits := disallowedRedirectHits.Load(); hits != 0 {
		t.Fatalf("redirect destination escaped proxy %d times", hits)
	}
	if hits := disallowedResourceHits.Load(); hits != 0 {
		t.Fatalf("subresource request escaped proxy %d times", hits)
	}
	if hits := disallowedIframeHits.Load(); hits != 0 {
		t.Fatalf("iframe navigation escaped proxy %d times", hits)
	}
	redirectBlocked := false
	resourceDecisionSeen := false
	iframeDecisionSeen := false
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		value, evalErr := page.EvaluateContext(ctx, `document.body && document.body.dataset.redirect || ''`)
		redirectBlocked = evalErr == nil && fmt.Sprint(value) == "blocked"
		resourceDecisionSeen = strings.Contains(proxyLogs.String(), "blocked CONNECT authority denied-resource.localhost:")
		iframeDecisionSeen = strings.Contains(proxyLogs.String(), "blocked CONNECT authority denied-iframe.localhost:")
		if redirectBlocked && resourceDecisionSeen && iframeDecisionSeen {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !redirectBlocked {
		t.Fatalf("redirect did not fail at the denied proxy destination; logs: %s", proxyLogs.String())
	}
	if !resourceDecisionSeen {
		t.Fatalf("proxy did not record a denied subresource destination; logs: %s", proxyLogs.String())
	}
	if !iframeDecisionSeen {
		t.Fatalf("proxy did not record a denied iframe destination; logs: %s", proxyLogs.String())
	}

	wssURL := "wss" + strings.TrimPrefix(disallowedWSURL, "https") + "/socket"
	if _, err := page.EvaluateContext(ctx, fmt.Sprintf(`new WebSocket(%q)`, wssURL)); err != nil {
		t.Fatalf("create WebSocket attempt: %v", err)
	}
	deniedAuthority := fmt.Sprintf("denied-ws.localhost:%d", portOf(t, disallowedWSURL))
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && !strings.Contains(proxyLogs.String(), "blocked CONNECT authority "+deniedAuthority) {
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(proxyLogs.String(), "blocked CONNECT authority "+deniedAuthority) {
		t.Fatalf("proxy did not log rejected WSS tunnel to %s: %s", deniedAuthority, proxyLogs.String())
	}
	allowedWSURL := "wss" + strings.TrimPrefix(allowedURL, "https") + "/socket"
	if value, evalErr := page.EvaluateContext(ctx, fmt.Sprintf(`new Promise((resolve,reject)=>{
  const ws=new WebSocket(%q);
  const timer=setTimeout(()=>reject(new Error('allowed WSS handshake timed out')),3000);
  ws.addEventListener('open',()=>{clearTimeout(timer);ws.close();resolve('opened')},{once:true});
  ws.addEventListener('error',()=>{clearTimeout(timer);reject(new Error('allowed WSS handshake failed'))},{once:true});
})`, allowedWSURL)); evalErr != nil || value != "opened" {
		t.Fatalf("allowed WSS handshake result=%v err=%v; proxy logs: %s", value, evalErr, proxyLogs.String())
	}
	if hits := allowedWSHits.Load(); hits != 1 {
		t.Fatalf("allowed WebSocket destination received %d handshakes, want 1", hits)
	}
	if hits := disallowedWSHits.Load(); hits != 0 {
		t.Fatalf("WebSocket handshake escaped proxy %d times", hits)
	}
	if hits := disallowedRedirectHits.Load(); hits != 0 {
		t.Fatalf("redirect destination escaped proxy %d times", hits)
	}
	if hits := disallowedResourceHits.Load(); hits != 0 {
		t.Fatalf("subresource destination escaped proxy %d times", hits)
	}
	if hits := disallowedIframeHits.Load(); hits != 0 {
		t.Fatalf("iframe destination escaped proxy %d times", hits)
	}

	newTab, err := engine.NewPage()
	if err != nil {
		t.Fatalf("open second browser target: %v", err)
	}
	defer newTab.Close()
	if err := newTab.NavigateContext(ctx, disallowedNewTabURL); err != nil {
		t.Logf("navigation to denied new-tab origin returned expected browser error: %v", err)
	}
	newTabAuthority := fmt.Sprintf("blocked CONNECT authority denied-new-tab.localhost:%d", portOf(t, disallowedNewTabURL))
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && !strings.Contains(proxyLogs.String(), newTabAuthority) {
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(proxyLogs.String(), newTabAuthority) {
		t.Fatalf("proxy did not record denied new-tab navigation: %s", proxyLogs.String())
	}
	if hits := disallowedNewTabHits.Load(); hits != 0 {
		t.Fatalf("new-tab navigation escaped proxy %d times", hits)
	}

	decisions := parseProxyDecisions(proxyLogs.String())
	assertDecision := func(host string, port int, allowed bool) {
		t.Helper()
		for _, decision := range decisions {
			if decision["event"] == "connect" && decision["host"] == host && decision["port"] == float64(port) && decision["allowed"] == allowed {
				return
			}
		}
		t.Fatalf("missing structured connect decision for %s:%d allowed=%v: %#v", host, port, allowed, decisions)
	}
	assertDecision("allowed.localhost", allowedPort, true)
	assertDecision("denied.localhost", portOf(t, disallowedURL), false)
	assertDecision("denied-redirect.localhost", portOf(t, disallowedRedirectBaseURL), false)
	assertDecision("denied-resource.localhost", portOf(t, disallowedResourceURL), false)
	assertDecision("denied-iframe.localhost", portOf(t, disallowedIframeURL), false)
	assertDecision("denied-new-tab.localhost", portOf(t, disallowedNewTabURL), false)
	assertDecision("denied-ws.localhost", portOf(t, disallowedWSURL), false)

	evidence := struct {
		SchemaVersion   int              `json:"schema_version"`
		Scenario        string           `json:"scenario"`
		Verified        bool             `json:"verified"`
		Browser         string           `json:"browser"`
		Proxy           string           `json:"proxy"`
		OS              string           `json:"os"`
		Decisions       []map[string]any `json:"decisions"`
		DestinationHits map[string]int32 `json:"destination_hits"`
	}{
		SchemaVersion: 1,
		Scenario:      "egress-proxy-service-worker-redirect-subresource-iframe-new-tab-and-websocket",
		Verified:      true,
		Browser:       strings.TrimSpace(string(commandOutput(chrome, "--version"))),
		Proxy:         strings.TrimSpace(string(commandOutput(mitmdump, "--version"))),
		OS:            runtime.GOOS + "/" + runtime.GOARCH,
		Decisions:     decisions,
		DestinationHits: map[string]int32{
			"allowed_websocket":   allowedWSHits.Load(),
			"service_worker_http": disallowedHTTPHits.Load(),
			"redirect":            disallowedRedirectHits.Load(),
			"subresource":         disallowedResourceHits.Load(),
			"iframe":              disallowedIframeHits.Load(),
			"new_tab":             disallowedNewTabHits.Load(),
			"websocket":           disallowedWSHits.Load(),
		},
	}
	encodedEvidence, err := json.MarshalIndent(evidence, "", "  ")
	if err != nil {
		t.Fatalf("encode egress evidence: %v", err)
	}
	compactEvidence, err := json.Marshal(evidence)
	if err != nil {
		t.Fatalf("encode compact egress evidence: %v", err)
	}
	t.Logf("SCOUT_EGRESS_INTEGRATION_EVIDENCE %s", compactEvidence)
	if evidencePath := os.Getenv("SCOUT_EGRESS_EVIDENCE_PATH"); evidencePath != "" {
		if err := os.WriteFile(evidencePath, append(encodedEvidence, '\n'), 0o600); err != nil {
			t.Fatalf("write egress evidence to %s: %v", evidencePath, err)
		}
	}
}

// This destination-side fixture checks fail-closed proxy behavior for HTTP and
// the channel HTTP proxy counters cannot see: WebRTC's direct STUN UDP socket.
func TestIntegrationProxyLaunchBlocksDirectEgressOnProxyFailure(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	if _, err := findChromeForEgressTest(); err != nil {
		t.Skip("Chrome not installed")
	}

	stunSink, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer stunSink.Close()
	stunPort := stunSink.LocalAddr().(*net.UDPAddr).Port
	stunTCP, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer stunTCP.Close()
	stunTCPPort := stunTCP.Addr().(*net.TCPAddr).Port
	var tcpDestinationHits atomic.Int32
	stopTCPSink := make(chan struct{})
	go func() {
		for {
			_ = stunTCP.(*net.TCPListener).SetDeadline(time.Now().Add(100 * time.Millisecond))
			conn, acceptErr := stunTCP.Accept()
			if acceptErr == nil {
				tcpDestinationHits.Add(1)
				_ = conn.Close()
				continue
			}
			select {
			case <-stopTCPSink:
				return
			default:
			}
		}
	}()
	defer close(stopTCPSink)
	var destinationHits atomic.Int32
	stopSink := make(chan struct{})
	go func() {
		buf := make([]byte, 2048)
		for {
			_ = stunSink.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
			if _, _, readErr := stunSink.ReadFrom(buf); readErr == nil {
				destinationHits.Add(1)
			} else {
				select {
				case <-stopSink:
					return
				default:
				}
			}
		}
	}()
	defer close(stopSink)
	var httpDestinationHits atomic.Int32
	httpSink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		httpDestinationHits.Add(1)
		_, _ = w.Write([]byte("must not bypass configured proxy"))
	}))
	defer httpSink.Close()

	// Confirm the TCP TURN destination is a live oracle: an unproxied browser
	// must attempt a connection before the proxy-policy assertion is meaningful.
	baseline := New(WithHeadless(true), WithAllowPrivateIPs(true))
	baselineCtx, cancelBaseline := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancelBaseline()
	if err := baseline.LaunchContext(baselineCtx); err != nil {
		t.Fatalf("launch direct Chrome baseline: %v", err)
	}
	baselinePage, err := baseline.NewPage()
	if err != nil {
		_ = baseline.Close()
		t.Fatalf("create direct baseline page: %v", err)
	}
	_, probeErr := baselinePage.EvaluateContext(baselineCtx, fmt.Sprintf(`(async()=>{try{
  const pc=new RTCPeerConnection({iceServers:[{urls:"turn:127.0.0.1:%d?transport=tcp",username:"probe",credential:"probe"}]});
  pc.createDataChannel("tcp-baseline");
  await pc.setLocalDescription(await pc.createOffer());
  await new Promise(resolve=>{
    if(pc.iceGatheringState==="complete") return resolve();
    pc.addEventListener("icegatheringstatechange",()=>{if(pc.iceGatheringState==="complete")resolve()});
    setTimeout(resolve,5000);
  });
  pc.close();return true;
  }catch(e){return {error:String(e)}}
})()`, stunTCPPort))
	_ = baselinePage.Close()
	_ = baseline.Close()
	if probeErr != nil {
		t.Fatalf("run direct WebRTC TCP TURN baseline: %v", probeErr)
	}
	time.Sleep(300 * time.Millisecond)
	if hits := tcpDestinationHits.Load(); hits == 0 {
		t.Skip("Chrome did not attempt the direct TCP TURN baseline; TCP proxy assertion is inconclusive")
	}
	tcpDestinationHits.Store(0)

	engine := New(WithProxy("http://127.0.0.1:9"), WithHeadless(true), WithAllowPrivateIPs(true))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := engine.LaunchContext(ctx); err != nil {
		t.Fatalf("launch proxied Chrome: %v", err)
	}
	defer engine.Close()
	page, err := engine.NewPage()
	if err != nil {
		t.Fatalf("create page: %v", err)
	}
	defer page.Close()
	if err := page.NavigateContext(ctx, httpSink.URL); err == nil {
		t.Fatal("navigation unexpectedly succeeded while the configured proxy was unavailable")
	}
	if hits := httpDestinationHits.Load(); hits != 0 {
		t.Fatalf("%d HTTP requests reached the destination without the configured proxy", hits)
	}
	_, err = page.EvaluateContext(ctx, fmt.Sprintf(`(async()=>{
  const pc=new RTCPeerConnection({iceServers:[{urls:"stun:127.0.0.1:%d"}]});
  pc.createDataChannel("probe");
  await pc.setLocalDescription(await pc.createOffer());
  await new Promise(resolve=>{
    if(pc.iceGatheringState==="complete") return resolve();
    pc.addEventListener("icegatheringstatechange",()=>{
      if(pc.iceGatheringState==="complete") resolve();
    });
    setTimeout(resolve,5000);
  });
  const result={state:pc.iceGatheringState, candidates:pc.localDescription?.sdp.match(/a=candidate:/g)?.length||0};
  pc.close(); return result;
})()`, stunPort))
	if err != nil {
		t.Fatalf("run WebRTC STUN probe: %v", err)
	}
	// Allow delayed ICE work to reach the independent UDP destination oracle.
	time.Sleep(300 * time.Millisecond)
	if hits := destinationHits.Load(); hits != 0 {
		t.Fatalf("WebRTC sent %d UDP datagrams directly to the STUN destination despite proxy policy", hits)
	}
	tcpProbe, err := page.EvaluateContext(ctx, fmt.Sprintf(`(async()=>{try{
  const pc=new RTCPeerConnection({iceServers:[{urls:"turn:127.0.0.1:%d?transport=tcp",username:"probe",credential:"probe"}]});
  pc.createDataChannel("tcp-probe");
  await pc.setLocalDescription(await pc.createOffer());
  await new Promise(resolve=>{
    if(pc.iceGatheringState==="complete") return resolve();
    pc.addEventListener("icegatheringstatechange",()=>{if(pc.iceGatheringState==="complete")resolve()});
    setTimeout(resolve,5000);
  });
  const result={state:pc.iceGatheringState,candidates:pc.localDescription?.sdp.match(/a=candidate:/g)?.length||0};
  pc.close();return result;
  }catch(e){return {error:String(e)}}
})()`, stunTCPPort))
	if err != nil {
		t.Fatalf("run WebRTC TCP STUN probe: %v", err)
	}
	t.Logf("WebRTC TCP STUN probe result: %v", tcpProbe)
	time.Sleep(300 * time.Millisecond)
	if hits := tcpDestinationHits.Load(); hits != 0 {
		t.Fatalf("WebRTC sent %d TCP connections directly to the STUN destination despite proxy policy", hits)
	}
	if hits := httpDestinationHits.Load(); hits != 0 {
		t.Fatalf("HTTP destination received %d direct requests after proxy failure", hits)
	}
}

// A proxy hostname is the only name the locally launched browser should need
// to resolve. Destination hostnames are left for the proxy to resolve, and the
// page still loads even though the browser resolver maps all other names to
// NOTFOUND.
func TestIntegrationProxyHostResolverKeepsDestinationDNSUpstream(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	if _, err := findChromeForEgressTest(); err != nil {
		t.Skip("Chrome not installed")
	}

	const destinationHost = "scout-upstream-resolution.invalid"
	var proxyRequests atomic.Int32
	var requestHost atomic.Value
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Host == destinationHost {
			proxyRequests.Add(1)
			requestHost.Store(r.URL.Host)
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<!doctype html><title>proxied without local destination DNS</title>"))
	}))
	defer proxy.Close()
	proxyURL := replaceLoopbackHost(proxy.URL, "proxy.localhost")

	engine := New(WithProxy(proxyURL), WithHeadless(true), WithAllowPrivateIPs(true))
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := engine.LaunchContext(ctx); err != nil {
		t.Fatalf("launch proxied Chrome: %v", err)
	}
	defer engine.Close()
	page, err := engine.NewPage()
	if err != nil {
		t.Fatalf("create page: %v", err)
	}
	defer page.Close()
	if err := page.NavigateContext(ctx, "http://"+destinationHost+"/"); err != nil {
		t.Fatalf("navigation did not reach proxy with browser DNS restricted: %v", err)
	}
	if hits := proxyRequests.Load(); hits == 0 {
		t.Fatal("proxy did not receive a request for the unresolved destination hostname")
	}
	if got, _ := requestHost.Load().(string); got != destinationHost {
		t.Fatalf("proxy received destination host %q, want %q", got, destinationHost)
	}
}

// An origin name can be allowlisted while resolving to a private endpoint.
// This fixture verifies the proxy denies it before the destination receives
// HTTP bytes, independently of Scout's page-level URL policy.
func TestIntegrationEgressProxyBlocksAllowedHostnameResolvingToLoopback(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome and mitmproxy")
	}
	mitmdump, err := exec.LookPath("mitmdump")
	if err != nil {
		t.Skip("mitmdump not installed")
	}
	chrome, err := findChromeForEgressTest()
	if err != nil {
		t.Skip("Chrome not installed")
	}

	var destinationHits atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		destinationHits.Add(1)
		_, _ = w.Write([]byte("must not be reached"))
	}))
	defer destination.Close()
	origin := replaceLoopbackHost(destination.URL, "allowed.localhost")
	originURL, err := url.Parse(origin)
	if err != nil {
		t.Fatal(err)
	}
	proxyPort := freeTCPPort(t)
	var proxyLogs lockedBuffer
	proxy := exec.Command(mitmdump,
		"--mode", "regular",
		"--listen-host", "127.0.0.1",
		"--listen-port", fmt.Sprint(proxyPort),
		"-s", filepath.Join("examples", "egress-proxy", "allowlist.py"),
	)
	proxy.Env = append(os.Environ(), "SCOUT_ALLOWED_ORIGINS="+origin)
	proxy.Stdout = &proxyLogs
	proxy.Stderr = &proxyLogs
	if err := proxy.Start(); err != nil {
		t.Fatalf("start mitmdump: %v", err)
	}
	defer stopProcess(proxy)
	waitForTCP(t, fmt.Sprintf("127.0.0.1:%d", proxyPort), 10*time.Second, &proxyLogs)

	chromePort := freeTCPPort(t)
	profile, err := os.MkdirTemp("", "scout-egress-private-peer-chrome-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(profile)
	browser := exec.Command(chrome,
		"--headless=new", "--no-sandbox", "--disable-gpu",
		"--remote-debugging-address=127.0.0.1", "--remote-debugging-port="+fmt.Sprint(chromePort),
		"--remote-allow-origins=*", "--user-data-dir="+profile,
		"--proxy-server=http://127.0.0.1:"+fmt.Sprint(proxyPort),
		"--proxy-bypass-list=<-loopback>",
		"--host-resolver-rules=MAP allowed.localhost 127.0.0.1",
		"about:blank",
	)
	browser.Stdout = os.Stdout
	browser.Stderr = os.Stderr
	if err := browser.Start(); err != nil {
		t.Fatalf("start Chrome: %v", err)
	}
	defer stopProcess(browser)
	cdpURL := waitForCDP(t, chromePort, 20*time.Second)

	engine := New(WithRemoteCDP(cdpURL), WithHeadless(true), WithAllowPrivateIPs(true))
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := engine.LaunchContext(ctx); err != nil {
		t.Fatalf("attach Scout to Chrome: %v", err)
	}
	defer engine.Close()
	page, err := engine.NewPage()
	if err != nil {
		t.Fatalf("create page: %v", err)
	}
	defer page.Close()
	_ = page.NavigateContext(ctx, origin)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && !strings.Contains(proxyLogs.String(), `"reason":"private_peer_denied"`) {
		time.Sleep(25 * time.Millisecond)
	}
	if !strings.Contains(proxyLogs.String(), `"reason":"private_peer_denied"`) {
		t.Fatalf("proxy did not record private resolved peer denial: %s", proxyLogs.String())
	}
	if hits := destinationHits.Load(); hits != 0 {
		t.Fatalf("allowlisted private destination received %d requests", hits)
	}
	if !strings.Contains(proxyLogs.String(), `"resolved_ip":"127.0.0.1"`) && !strings.Contains(proxyLogs.String(), `"resolved_ip":"::1"`) {
		t.Fatalf("proxy decision did not record a loopback destination IP: %s", proxyLogs.String())
	}
	if originURL.Port() == "" {
		t.Fatalf("fixture origin is missing its explicit test port: %s", origin)
	}
}

func commandOutput(command string, args ...string) []byte {
	output, err := exec.Command(command, args...).CombinedOutput()
	if err != nil {
		return []byte("unknown")
	}
	return output
}

func parseProxyDecisions(raw string) []map[string]any {
	const marker = "SCOUT_EGRESS_DECISION "
	var decisions []map[string]any
	for _, line := range strings.Split(raw, "\n") {
		index := strings.Index(line, marker)
		if index < 0 {
			continue
		}
		var decision map[string]any
		if json.Unmarshal([]byte(line[index+len(marker):]), &decision) == nil {
			decisions = append(decisions, decision)
		}
	}
	return decisions
}

func freeTCPPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	return port
}

func waitForTCP(t *testing.T, address string, timeout time.Duration, logs *lockedBuffer) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", address, 100*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("proxy did not listen on %s: %s", address, logs.String())
}

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

func waitForCDP(t *testing.T, port int, timeout time.Duration) string {
	t.Helper()
	client := &http.Client{Timeout: time.Second, Transport: &http.Transport{Proxy: nil}}
	deadline := time.Now().Add(timeout)
	url := fmt.Sprintf("http://127.0.0.1:%d/json/version", port)
	for time.Now().Before(deadline) {
		response, err := client.Get(url)
		if err == nil {
			var version struct {
				WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
			}
			decodeErr := json.NewDecoder(response.Body).Decode(&version)
			_ = response.Body.Close()
			if decodeErr == nil && version.WebSocketDebuggerURL != "" {
				return version.WebSocketDebuggerURL
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("Chrome DevTools endpoint did not start on port %d", port)
	return ""
}

func replaceLoopbackHost(rawURL, host string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	_, port, err := net.SplitHostPort(parsed.Host)
	if err != nil {
		return rawURL
	}
	parsed.Host = net.JoinHostPort(host, port)
	return parsed.String()
}

func portOf(t *testing.T, rawURL string) int {
	t.Helper()
	_, port, err := net.SplitHostPort(strings.TrimPrefix(strings.TrimPrefix(rawURL, "https://"), "http://"))
	if err != nil {
		t.Fatal(err)
	}
	var value int
	if _, err := fmt.Sscan(port, &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func stopProcess(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
}

func findChromeForEgressTest() (string, error) {
	for _, name := range []string{"google-chrome", "chromium", "chromium-browser", "chrome"} {
		if path, err := exec.LookPath(name); err == nil {
			return path, nil
		}
	}
	if runtime.GOOS == "darwin" {
		for _, path := range []string{
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
			"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
		} {
			if _, err := os.Stat(path); err == nil {
				return path, nil
			}
		}
	}
	return "", fmt.Errorf("no supported Chrome executable found")
}
