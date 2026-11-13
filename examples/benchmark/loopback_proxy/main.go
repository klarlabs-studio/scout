// Command loopback_proxy verifies that Scout-launched Chrome sends loopback
// destinations through a configured HTTP proxy instead of Chromium's implicit
// loopback bypass.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"time"

	browse "go.klarlabs.de/scout"
)

type scenarioResult struct {
	Scheme          string `json:"scheme"`
	Host            string `json:"host"`
	TargetRequest   string `json:"target_request,omitempty"`
	ProxySawTarget  bool   `json:"proxy_saw_target"`
	ProxyStatus     int    `json:"proxy_status,omitempty"`
	OriginHits      int32  `json:"origin_hits"`
	NavigationError string `json:"navigation_error,omitempty"`
	Passed          bool   `json:"passed"`
	Error           string `json:"error,omitempty"`
}

func main() {
	results := make([]scenarioResult, 0, 4)
	for _, host := range []string{"127.0.0.1", "localhost", "::1"} {
		result := run(host)
		results = append(results, result)
		if !result.Passed {
			_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"scenario": "scout-loopback-proxy-enforcement", "runs": results})
			os.Exit(1)
		}
	}
	httpsResult := runHTTPSConnect()
	results = append(results, httpsResult)
	if !httpsResult.Passed {
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"scenario": "scout-loopback-proxy-enforcement", "runs": results})
		os.Exit(1)
	}
	_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"scenario": "scout-loopback-proxy-enforcement", "runs": results})
}

func run(host string) scenarioResult {
	result := scenarioResult{Scheme: "http", Host: host}
	var originHits atomic.Int32
	originListener, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
	if err != nil {
		result.Error = "listen origin: " + err.Error()
		return result
	}
	defer originListener.Close()
	origin := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		originHits.Add(1)
		_, _ = io.WriteString(w, "origin reached")
	})}
	go func() { _ = origin.Serve(originListener) }()
	defer origin.Close()
	originAddress := originListener.Addr().String()
	originURL := "http://" + originAddress + "/"
	parsedOrigin, err := url.Parse(originURL)
	if err != nil {
		result.Error = "parse origin URL: " + err.Error()
		return result
	}

	proxyListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		result.Error = "listen proxy: " + err.Error()
		return result
	}
	defer proxyListener.Close()
	targetSeen := make(chan string, 1)
	go serveDenyProxy(proxyListener, parsedOrigin.Host, targetSeen)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	engine := browse.New(
		browse.WithHeadless(true),
		browse.WithAllowPrivateIPs(true),
		browse.WithProxy("http://"+proxyListener.Addr().String()),
	)
	if err := engine.LaunchContext(ctx); err != nil {
		result.Error = "launch Scout browser: " + err.Error()
		return result
	}
	defer engine.Close()
	_, navigationErr := engine.NewPageAt(originURL)
	if navigationErr != nil {
		// A proxy-generated 403 may be surfaced as a navigation error by some
		// Chrome versions; the destination-side evidence below is authoritative.
		result.NavigationError = navigationErr.Error()
	}
	select {
	case request := <-targetSeen:
		result.ProxySawTarget = true
		result.TargetRequest = request
		result.ProxyStatus = http.StatusForbidden
	case <-time.After(5 * time.Second):
		if result.Error == "" {
			result.Error = "proxy did not see the target request"
		}
	}
	result.OriginHits = originHits.Load()
	result.Passed = result.ProxySawTarget && result.OriginHits == 0
	if !result.Passed && result.Error == "" {
		result.Error = fmt.Sprintf("proxy_saw_target=%t origin_hits=%d", result.ProxySawTarget, result.OriginHits)
	}
	return result
}

func runHTTPSConnect() scenarioResult {
	result := scenarioResult{Scheme: "https", Host: "127.0.0.1"}
	var originHits atomic.Int32
	origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		originHits.Add(1)
		_, _ = io.WriteString(w, "TLS origin reached")
	}))
	defer origin.Close()
	parsedOrigin, err := url.Parse(origin.URL)
	if err != nil {
		result.Error = "parse TLS origin URL: " + err.Error()
		return result
	}
	proxyListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		result.Error = "listen proxy: " + err.Error()
		return result
	}
	defer proxyListener.Close()
	targetSeen := make(chan string, 1)
	go serveDenyProxy(proxyListener, parsedOrigin.Host, targetSeen)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	engine := browse.New(
		browse.WithHeadless(true),
		browse.WithAllowPrivateIPs(true),
		browse.WithProxy("http://"+proxyListener.Addr().String()),
	)
	if err := engine.LaunchContext(ctx); err != nil {
		result.Error = "launch Scout browser: " + err.Error()
		return result
	}
	defer engine.Close()
	_, navigationErr := engine.NewPageAt(origin.URL)
	if navigationErr != nil {
		result.NavigationError = navigationErr.Error()
	}
	select {
	case request := <-targetSeen:
		result.ProxySawTarget = true
		result.TargetRequest = request
		result.ProxyStatus = http.StatusForbidden
	case <-time.After(5 * time.Second):
		if result.Error == "" {
			result.Error = "proxy did not see the HTTPS CONNECT request"
		}
	}
	result.OriginHits = originHits.Load()
	result.Passed = result.ProxySawTarget && result.OriginHits == 0 && strings.HasPrefix(result.TargetRequest, "CONNECT ")
	if !result.Passed && result.Error == "" {
		result.Error = fmt.Sprintf("proxy_saw_connect=%t origin_hits=%d request=%q", result.ProxySawTarget, result.OriginHits, result.TargetRequest)
	}
	return result
}

func serveDenyProxy(listener net.Listener, targetAuthority string, targetSeen chan<- string) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		go func() {
			defer conn.Close()
			request, err := http.ReadRequest(bufio.NewReader(conn))
			if err != nil {
				return
			}
			authority := request.Host
			if request.Method != http.MethodConnect && request.URL != nil {
				authority = request.URL.Host
			}
			if authority == targetAuthority {
				select {
				case targetSeen <- request.Method + " " + authority:
				default:
				}
			}
			body := "denied by Scout loopback proxy probe"
			_, _ = fmt.Fprintf(conn, "HTTP/1.1 403 Forbidden\r\nContent-Type: text/plain\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", len(body), body)
		}()
	}
}
