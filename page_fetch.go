package browse

import (
	"net/url"
	"sync"
)

// RequestRule inspects intercepted requests and decides what happens to each.
// Rules are consulted in registration order: the first to Block fails the
// request; otherwise every rule's AddHeaders are merged onto a continued
// request. This lets independent concerns — URL policy, resource blocking,
// header injection — share a single CDP Fetch session instead of fighting over
// Fetch.enable (only one owner can, and each paused request must be resolved
// exactly once).
type RequestRule struct {
	Name string
	// ResourceTypes limits which CDP resource types are intercepted for this
	// rule (e.g. "Document", "Image"). Empty means all types. This only shapes
	// the Fetch pattern set; a rule must still self-filter in Decide because
	// another rule may have widened interception.
	ResourceTypes []string
	Decide        func(InterceptedRequest) RequestVerdict
}

// InterceptedRequest is the subset of a paused CDP request a rule sees.
type InterceptedRequest struct {
	URL          string
	Method       string
	ResourceType string
	Headers      map[string]string
	// TopLevelOrigin is the origin of the last caller-initiated navigation, used
	// to scope header injection to the intended target (not cross-origin
	// subresources or redirect destinations).
	TopLevelOrigin string
}

// RequestVerdict is a rule's decision for one intercepted request.
type RequestVerdict struct {
	Block       bool
	BlockReason string            // CDP errorReason; defaults to BlockedByClient
	AddHeaders  map[string]string // merged onto the continued request's headers
}

// fetchInterceptor is the single owner of CDP Fetch interception for a page.
type fetchInterceptor struct {
	page  *Page
	mu    sync.Mutex
	rules []RequestRule
	unsub func()
	on    bool
}

// InterceptRequests registers a request-interception rule and returns a func to
// remove it. Registering the first rule enables Fetch; removing the last
// disables it.
func (p *Page) InterceptRequests(rule RequestRule) (func(), error) {
	fi := p.fetchInterceptor()
	fi.mu.Lock()
	fi.rules = append(fi.rules, rule)
	err := fi.syncLocked()
	fi.mu.Unlock()
	if err != nil {
		p.removeRule(rule.Name)
		return func() {}, err
	}
	var once sync.Once
	return func() { once.Do(func() { p.removeRule(rule.Name) }) }, nil
}

func (p *Page) fetchInterceptor() *fetchInterceptor {
	if p.fetch == nil {
		p.fetch = &fetchInterceptor{page: p}
	}
	return p.fetch
}

func (p *Page) removeRule(name string) {
	fi := p.fetch
	if fi == nil {
		return
	}
	fi.mu.Lock()
	defer fi.mu.Unlock()
	for i, r := range fi.rules {
		if r.Name == name {
			fi.rules = append(fi.rules[:i], fi.rules[i+1:]...)
			break
		}
	}
	_ = fi.syncLocked()
}

// syncLocked reconciles the CDP Fetch state with the current rule set. Caller
// holds fi.mu.
func (fi *fetchInterceptor) syncLocked() error {
	if len(fi.rules) == 0 {
		if fi.on {
			_, _ = fi.page.call("Fetch.disable", nil)
			if fi.unsub != nil {
				fi.unsub()
				fi.unsub = nil
			}
			fi.on = false
		}
		return nil
	}
	if _, err := fi.page.call("Fetch.enable", map[string]any{"patterns": fi.patternsLocked()}); err != nil {
		return err
	}
	if fi.unsub == nil {
		fi.unsub = fi.page.OnSession("Fetch.requestPaused", fi.handle)
	}
	fi.on = true
	return nil
}

// patternsLocked builds the Fetch pattern set as the union of the rules' types;
// any rule wanting all types collapses it to a single catch-all pattern.
func (fi *fetchInterceptor) patternsLocked() []map[string]any {
	seen := map[string]struct{}{}
	for _, r := range fi.rules {
		if len(r.ResourceTypes) == 0 {
			return []map[string]any{{}} // all requests
		}
		for _, t := range r.ResourceTypes {
			seen[t] = struct{}{}
		}
	}
	out := make([]map[string]any, 0, len(seen))
	for t := range seen {
		out = append(out, map[string]any{"resourceType": t})
	}
	return out
}

// handle resolves one paused request. It runs on the CDP dispatch goroutine
// (off the read loop), so the continue/fail CDP calls it makes here don't
// deadlock.
func (fi *fetchInterceptor) handle(params map[string]any) {
	reqID, _ := params["requestId"].(string)
	if reqID == "" {
		return
	}
	reqObj, _ := params["request"].(map[string]any)
	req := InterceptedRequest{
		ResourceType:   asString(params["resourceType"]),
		TopLevelOrigin: fi.page.topLevelOrigin(),
	}
	if reqObj != nil {
		req.URL = asString(reqObj["url"])
		req.Method = asString(reqObj["method"])
		req.Headers = asStringMap(reqObj["headers"])
	}

	fi.mu.Lock()
	rules := append([]RequestRule(nil), fi.rules...)
	fi.mu.Unlock()

	merged := map[string]string{}
	for _, r := range rules {
		v := r.Decide(req)
		if v.Block {
			reason := v.BlockReason
			if reason == "" {
				reason = "BlockedByClient"
			}
			_, _ = fi.page.call("Fetch.failRequest", map[string]any{"requestId": reqID, "errorReason": reason})
			return
		}
		for k, val := range v.AddHeaders {
			merged[k] = val
		}
	}

	cont := map[string]any{"requestId": reqID}
	if len(merged) > 0 {
		all := map[string]string{}
		for k, v := range req.Headers {
			all[k] = v
		}
		for k, v := range merged {
			all[k] = v
		}
		entries := make([]map[string]string, 0, len(all))
		for k, v := range all {
			entries = append(entries, map[string]string{"name": k, "value": v})
		}
		cont["headers"] = entries
	}
	_, _ = fi.page.call("Fetch.continueRequest", cont)
}

// installURLPolicy adds the redirect/navigation guard: every Document request
// (initial navigation and each redirect) is re-validated against the page's URL
// policy, so a public URL that redirects to an internal host is blocked before
// Chrome ever fetches it. Only meaningful when the validator blocks private IPs.
func (p *Page) installURLPolicy() error {
	_, err := p.InterceptRequests(RequestRule{
		Name:          "url-policy",
		ResourceTypes: []string{"Document"},
		Decide: func(r InterceptedRequest) RequestVerdict {
			if r.ResourceType != "Document" {
				return RequestVerdict{}
			}
			if err := p.urlValidator.Validate(r.URL); err != nil {
				return RequestVerdict{Block: true, BlockReason: "AccessDenied"}
			}
			return RequestVerdict{}
		},
	})
	return err
}

func (p *Page) topLevelOrigin() string {
	p.navOriginMu.Lock()
	defer p.navOriginMu.Unlock()
	return p.navOrigin
}

func (p *Page) setTopLevelOrigin(origin string) {
	p.navOriginMu.Lock()
	p.navOrigin = origin
	p.navOriginMu.Unlock()
}

// SameOriginAsTop reports whether the request targets the same origin as the
// page's last caller-initiated navigation — used by header-injection rules to
// avoid leaking credentials to cross-origin subresources or redirect targets.
func (r InterceptedRequest) SameOriginAsTop() bool {
	return sameOrigin(r.URL, r.TopLevelOrigin)
}

func sameOrigin(rawURL, origin string) bool {
	if origin == "" {
		return false
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	return u.Scheme+"://"+u.Host == origin
}

// originOf returns scheme://host for a URL, or "" if it can't be parsed.
func originOf(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

func asString(v any) string {
	s, _ := v.(string)
	return s
}

func asStringMap(v any) map[string]string {
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, val := range m {
		if s, ok := val.(string); ok {
			out[k] = s
		}
	}
	return out
}
