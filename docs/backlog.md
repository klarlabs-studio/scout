
## Core Browser Automation Engine

Gin-like browser automation API built on pure CDP over WebSocket. No rod, no chromedp dependency. Includes Engine (browser lifecycle, global middleware, task registry), Context (page state, middleware chain control via Next/Abort, key-value data passing), Group (named task collections with inherited middleware), HandlerFunc middleware pattern, Selection/SelectionAll (fluent DOM element wrappers with Click/Input/Text/Attr/Visible/Value and wait methods). Supports concurrent task execution via WithPoolSize, remote CDP connections via WithRemoteCDP, URL validation with SSRF protection, and task lifecycle tracking via statekit state machine.

---

## Agent Package — AI-Optimized Session API

High-level session-based API for AI agents in the agent/ package. All methods are goroutine-safe (sync.Mutex), return JSON-serializable structs, and auto-wait for elements. Includes: Navigate, Click, ClickAndWait, Type, FillForm, Extract, ExtractAll, ExtractTable, HasElement, WaitFor, Eval, Screenshot (auto-compressed to MaxScreenshotBytes), PDF, Snapshot. Content distillation: Observe (structured links/inputs/buttons/text), ObserveDiff (MutationObserver-based DOM change tracking), Markdown (HTML→markdown conversion), ReadableText (main content extraction stripping nav/boilerplate), AccessibilityTree (semantic element tree). Token-budget-aware extraction via ObserveWithBudget. All output respects configurable ContentOptions (MaxLength, MaxLinks, MaxInputs, MaxButtons, MaxItems, MaxRows, MaxScreenshotBytes).

---

## Semantic Form Filling & Discovery

Auto-map human-readable field names to input elements without CSS selectors. DiscoverForm analyzes forms and returns field labels, types, selectors via label/aria-label/aria-labelledby/placeholder/adjacent text resolution. FillFormSemantic accepts map[string]string with keys like "Email", "Password" and uses weighted fuzzy matching (exact label 100, name/ID 90, substring 70, placeholder 50, type hint 40) to find the right input. Exported MatchFormField function for independent testing. Works across all HTML form patterns.

---

## Visual Grounding — Annotated Screenshots

AnnotatedScreenshot overlays numbered red labels on all interactive elements (links, buttons, inputs, selects, [role=button], [onclick], [tabindex]) and captures a screenshot. Returns the image data plus a mapping of label numbers to element info (selector, tag, type, text, href, coordinates, dimensions). ClickLabel clicks an element by its label number. Designed for multimodal LLMs (Claude, GPT-4o) that can reference elements visually by number instead of requiring CSS selectors. Labels are cleaned up after capture.

---

## Advanced Interaction Tools — Hover, Drag, Select, Scroll

Agent-level interaction tools beyond click/type: Hover (trigger CSS :hover states, reveal tooltips and dropdown menus), Drag and Drop (drag elements between containers, reorder lists), Select option from dropdown/select elements by visible text or value, Scroll to element or by pixel offset (for infinite scroll pages and lazy-loaded content), Focus element (trigger :focus states), Double-click, Right-click (context menus). Each returns structured results. MCP tools: hover, drag, select_option, scroll, focus, double_click, right_click. Essential for modern SPAs with hover-triggered menus, drag-and-drop interfaces, and infinite scroll feeds.

---

## Network Request/Response Capture

Capture XHR/fetch API responses as structured data for agent consumption. EnableNetworkCapture subscribes to CDP Network domain events (requestWillBeSent, responseReceived, loadingFinished) with URL substring pattern filtering. CapturedRequests returns NetworkCapture structs (URL, Method, Status, MimeType, RequestHeaders, ResponseHeaders, ResponseBody). Response bodies fetched via Network.getResponseBody, truncated to MaxLength. Agents read structured API responses directly instead of scraping rendered DOM — 10x more reliable and token-efficient.

---

## Framework Detection & State Extraction

Detect and introspect 19 frontend frameworks. DetectedFrameworks identifies active frameworks via global markers and DOM inspection (React, Vue 2/3, Angular, Svelte, SolidJS, Preact, Lit, Alpine.js, HTMX, Stimulus, Ember, Qwik, Next.js, Nuxt, Remix, SvelteKit, Gatsby, Astro). ComponentState extracts state/props from any framework component at a CSS selector — auto-detects the framework and reads fiber trees (React), __vue__ (Vue 2), setupState (Vue 3), $$ (Svelte), ng.getComponent (Angular), _x_dataStack (Alpine), renderRoot (Lit). GetAppState reads global stores: __NEXT_DATA__, __NUXT__, __remixContext, __sveltekit_data, __INITIAL_STATE__, Alpine._stores, htmx.config, hydration script tags. WaitForSPA waits for framework hydration. DispatchEvent triggers framework event handlers. WaitForRouteChange detects pushState/hashchange.

---

## Middleware System — Resilience, Auth, Stealth, Network

Composable Gin-style middleware powered by felixgeelhaar/fortify and felixgeelhaar/bolt. Resilience: Retry (exponential backoff with jitter), Timeout (context-aware deadline), CircuitBreaker (consecutive failure threshold), RateLimit (token bucket), Bulkhead (concurrency limiter). Auth: BearerAuth, BasicAuth, CookieAuth (uses browse.Cookie type), HeaderAuth. Anti-detection: Stealth (10 patches: navigator.webdriver, chrome.runtime, Permissions API, plugins, languages, WebGL vendor/renderer, attachShadow, outerWidth/outerHeight, screen.availWidth). Network: BlockResources (block Image/Font/Stylesheet via Fetch.enable), WaitNetworkIdle. Utilities: ScreenshotOnError (filesystem-safe task name), SlowMotion, Viewport. Logging via bolt (zero-alloc, atomic.Pointer). SaveIndex/RestoreIndex enables middleware chain replay for retry.

---

## Screenshot Compression & Media Capture

Screenshots auto-compressed for LLM context windows. ScreenshotWithOptions accepts MaxSize (bytes) — if exceeded, progressively re-captures as JPEG with quality 80→60→40→20 then downscales at 75%→50%→25% via CDP clip.scale. ScreenshotCompact defaults to 5MB limit. ScreenshotFullPage captures entire scrollable page. ScreenshotElement captures a single DOM element by bounding box. PDF generation via Page.printToPDF with landscape/scale/margins/pageRanges options. Video recording via Recorder: CDP Page.startScreencast captures frames, SaveVideo assembles to MP4 via ffmpeg, SaveGIF for animated GIFs. Frame acks run in goroutines with sync.WaitGroup to avoid blocking CDP readLoop.

---

## Persistent Browser Profiles

Save and restore browser state across sessions. CaptureProfile (domain operation) extracts cookies via Network.getCookies and localStorage via JS eval into a Profile struct. ApplyProfile restores them. SaveProfile/LoadProfile are convenience wrappers that serialize to JSON files with 0600 permissions. Enables agents to maintain login sessions across invocations without re-authenticating. Profile struct holds []browse.Cookie and map[string]string localStorage. Domain/infrastructure separation following DDD principles.

---

## MCP Server — scout mcp serve

Single-binary MCP server with 29+ tools, zero Node.js/Python runtime dependencies. Lazy session creation (browser starts on first tool use, not at startup). Runtime reconfiguration via 'configure' tool (switch headless/visible mode without restart). Tool categories: Navigation (navigate, observe, observe_diff, observe_with_budget), Interaction (click, click_label, type, fill_form, fill_form_semantic, dispatch_event, hover, drag, select_option, scroll), Extraction (extract, extract_all, extract_table, markdown, readable_text, accessibility_tree), Capture (screenshot, annotated_screenshot, pdf), Network (enable_network_capture, network_requests), Framework (wait_spa, detect_frameworks, component_state, app_state), Utility (has_element, wait_for, discover_form, configure). Eval gated behind SCOUT_ENABLE_EVAL env var. Built with felixgeelhaar/mcp-go.

---

## CLI — scout command-line interface

Full-featured CLI for one-shot browser operations without writing Go code. Every command launches Chrome, navigates, performs one action, outputs result, exits. Commands: scout navigate <url> (JSON), scout observe <url> (structured observation), scout markdown <url> (compact markdown), scout screenshot <url> --output file.png, scout pdf <url> --output file.pdf, scout extract <url> <selector>, scout eval <url> <expression>, scout form discover <url> [selector], scout frameworks <url>. CLI defaults to visible browser (--headless flag to hide). Global flags: --headless, --timeout=30s. Subcommands: scout mcp serve, scout version, scout help. Version injected via GoReleaser ldflags.

---

## MCP Elicitation Support

Support MCP elicitation protocol (v2.1.76+) to request structured input from the user mid-task via interactive dialogs. When scout encounters ambiguity (e.g., multiple matching form fields, unclear selector, authentication required), it can present a form to the user via elicitation instead of guessing or failing. Use cases: "Which email field did you mean?" with options, "Enter credentials for this site", "This page has 3 forms — which one?". Implement elicitation request/response in the mcp-go server handler. Add Elicitation and ElicitationResult hook support.

---

## MCP Channels — Proactive Push Notifications

Support MCP channels protocol (v2.1.80+) to push messages proactively into the AI session instead of waiting to be polled. Scout can push: DOM mutation alerts (page changed after a click), network request completion (API response received), page navigation events (SPA route change detected), form validation errors (submission failed), element appearance (waited-for element now visible), screenshot-on-error alerts. Eliminates the need for agents to repeatedly call observe/observe_diff — scout tells the agent when something interesting happens. Implement channel registration and message pushing in the mcp-go server.

---

## MCP Dynamic Tool Registration via list_changed

Support MCP list_changed notifications (v2.1.49+) to dynamically register and unregister tools based on current page state. Scout exposes only contextually relevant tools: show fill_form_semantic and discover_form only when a form exists on the page, show network_requests only after enable_network_capture has been called, show click_label only after annotated_screenshot has been taken, show component_state only when a framework is detected, show extract_table only when a table element exists. Reduces tool clutter in the agent's context window. Emit tools/list_changed notification after navigate, observe, and configure calls. Implement in mcp-go server by tracking page state and diffing available tool sets.

---

## MCP Tool Annotations

Add MCP tool annotations to all 29+ scout tools for better auto-approval and agent decision-making. Annotations per tool: readOnlyHint (true for observe, extract, markdown, screenshot, has_element, detect_frameworks — false for click, type, fill_form, navigate, dispatch_event), destructiveHint (false for all scout tools — browser automation is inherently non-destructive to the host), openWorldHint (true for navigate, eval — false for extract, fill_form which operate on known page state), estimatedCostHint (navigate: high — launches page load; observe: low — reads cached state; screenshot: medium — renders image; fill_form_semantic: medium — discovers + fills). Helps Claude Code make auto-approval decisions without prompting the user for read-only operations.

---

## MCP Structured Content Responses

Return structuredContent in MCP tool responses instead of serializing everything to text strings. Benefits: typed responses that MCP clients can render natively (tables, images, JSON trees), proper image content blocks for screenshots (instead of base64 data URIs in text), structured error responses with error codes and suggestions, rich metadata (timing, token estimates, truncation indicators) alongside the primary result. Implement outputSchema on tools that return structured data (observe, extract_table, discover_form, annotated_screenshot, network_requests, component_state, app_state). Plain text tools (markdown, readable_text, accessibility_tree) remain as text content.

---

## MCP Streaming Tool Results

Stream partial results for long-running tool operations instead of blocking until complete. Use MCP progress reporting for: navigate (stream load progress: DNS→connect→TLS→response→DOM→complete), extract_all on large result sets (stream items as they're found), extract_table on large tables (stream rows as they're extracted), network_requests (stream captured requests as they arrive), observe on complex pages (stream links, then inputs, then buttons incrementally). Reduces time-to-first-token for agents waiting on browser operations. Implement via mcp-go ProgressReporter with structured progress updates containing partial results.

---

## DOM Diff with Change Classification

Beyond raw DOM diff (Added/Removed/Modified), classify changes semantically: navigation (URL changed), content_loaded (new content appeared), form_validation_error (error messages appeared after form submit), modal_appeared (dialog/modal overlay detected), element_state_changed (button disabled, input validation), notification (toast/banner appeared), loading_complete (spinner removed, skeleton replaced). Agent receives "modal_appeared: Login required" instead of re-analyzing the entire page. Implement heuristic classifiers on top of MutationObserver data — check for new dialog/modal elements, URL changes, aria-live regions, error class patterns.

---

## Agent Action Replay — Deterministic Playback

Record an agent's successful browser session as a deterministic script, then replay without any LLM calls. RecordSession starts capturing all actions (navigate, click, type, extract) as a sequence of Action structs with selectors and expected outcomes. SavePlaybook serializes to JSON. ReplayPlaybook executes the recorded actions deterministically. When a step fails (element not found, wrong page state), emits a signal for the agent to re-plan from that point. Reduces cost by 100x on repeat workflows — agent figures out a flow once, scout records it, subsequent runs skip the LLM. MCP tool: save_playbook, replay_playbook.

---

## Shadow DOM Traversal

Traverse shadow DOM boundaries for modern web components (Lit, Shoelace, Material Web, corporate design systems). Use DOM.describeNode with pierce:true and DOM.getFlattenedDocument for shadow-inclusive DOM trees. QuerySelector and QuerySelectorAll should pierce shadow roots by default. AccessibilityTree and Observe should include elements inside shadow roots. Critical for automating sites using web components — without shadow DOM traversal, agents cannot see or interact with a growing portion of the modern web.

---

## Multi-Tab Coordination

Support coordinated multi-tab workflows where agents cross-reference data between tabs. Session manages multiple named pages (tabs). OpenTab creates a new tab. SwitchTab activates a named tab. ListTabs returns all open tabs with their URLs and titles. Shared key-value store across tabs via session.Set/Get. Use cases: compare prices on two sites simultaneously, copy data between applications, maintain context across multiple pages, login on one tab and use session on another. MCP tools: open_tab, switch_tab, list_tabs, close_tab.

---

## Action Cost Estimation

Return estimated cost metadata with Observe responses so agents can make better planning decisions. Each interactive element in the observation includes an estimated_cost field: navigation actions (clicking links) are expensive (full page load + re-observation), in-page clicks are cheap (DOM update only), form submissions are medium (may trigger navigation), extraction is free (no page state change). Helps agents decide whether to extract all data from the current page before clicking away, or whether to click immediately. Include timing estimates based on page complexity.

---

## Distribution — GoReleaser, Homebrew, Install Script

Cross-platform distribution via GoReleaser: builds for linux/darwin/windows x amd64/arm64, injects version+commit via ldflags, publishes GitHub Releases with SHA256 checksums, auto-updates Homebrew formula in felixgeelhaar/homebrew-tap. Install methods: brew install felixgeelhaar/tap/scout, curl install script (auto-detects OS/arch), go install github.com/felixgeelhaar/scout/cmd/scout@latest. GitHub Actions CI: test matrix (3 Go versions x 2 OS), lint, vet, build. Release workflow triggers on v* tags. GitHub Pages landing page at docs/index.html.

---

## WebDriver BiDi — Cross-Browser Support (Firefox, Safari)

Add WebDriver BiDi protocol support alongside CDP to enable Firefox and Safari automation. WebDriver BiDi is a W3C standard that works over WebSocket (like CDP) but is supported by Firefox (Marionette → BiDi), Chrome, and Safari (WebKit). Implementation: add internal/bidi/ package as an alternative to internal/cdp/, implement a BrowserProtocol interface that both CDP and BiDi satisfy, let Page delegate to the active protocol. Auto-detect browser type from the WebSocket endpoint and use the right protocol. This makes scout the first Go browser automation library with native cross-browser support via BiDi. Chromium browsers continue using CDP (faster, more features). Firefox and Safari use BiDi. Users select via WithBrowser("firefox") option or the launcher auto-detects installed browsers.

---

## MCP resilience, diagnostics, and network history improvements

Improve MCP session resilience and diagnostics: add explicit reset tool, automatic recovery after consecutive timeout failures, richer error context for navigation/CDP failures, status visibility endpoint for browser/session health, and request history ring buffer so network inspection can include recent requests captured before explicit enablement.

---

## Resolve security vulnerabilities

Resolve current security scanner findings by addressing dependency/container/IaC issues and removing or suppressing non-sensitive false positives without weakening production security. Acceptance criteria: no unhandled actionable findings remain, manifests stay buildable, and unit validation passes where practical.

---

## Tighten security scan hygiene

Tighten local security scanning so generated frontend dependency and build directories do not dominate nox results. Acceptance criteria: project scan excludes generated UI artifacts, security checks still cover source files and manifests, and existing verification remains green.

---

## Issue #5 — fill_form_semantic checkbox/radio support

GitHub issue felixgeelhaar/scout#5. fill_form_semantic skips input[type=checkbox] and input[type=radio]. observe inputs[] omits them. Extend semantic form filling to accept boolean values for checkboxes (and string values for radios) via fuzzy label match. Resolve checkbox even when nested inside parent <label>. Dispatch input + change events so Vue v-model / React onChange fires. Also include checkbox/radio entries in agent.Observe inputs slice with type:"checkbox|radio" and current checked state. Acceptance: filling boolean field toggles checkbox, downstream submit becomes enabled, success report shows value_set/value_observed. Unit test with HTML fixture covers checked→unchecked and unchecked→checked transitions.

---

## Issue #6 — screenshot default budget tightening

GitHub issue felixgeelhaar/scout#6. Default screenshot tool result blows MCP token cap with max_width=1024. Tighten ScreenshotCompact default MaxSize from 5MB to ~200KB to honor "auto-compressed for LLM contexts" claim. When budget can't fit, return downscaled image + warning {downsampled:true, original_size:..., final_size:...} instead of failing. Update tool description to state explicit default budget. Acceptance: screenshot at max_width=1024 on rich page returns base64 under 200KB, never trips harness token cap. Adds quality/scale used into response metadata so callers can detect lossy capture.

---

## Issue #7 — observe active route/tab introspection

GitHub issue felixgeelhaar/scout#7. observe lacks active-route/tab info — SPAs that fall back silently when query-string tab id is invalid leave caller blind. Extend observe response with active_tab (text + id derived from [role=tab][aria-selected=true] or [aria-current=page]) and active_navigation breadcrumb (chain of [aria-current=page] / .active nav links + page H1). Heuristics: aria-selected, aria-current, role=tab, .active class on nav links. Acceptance: observe on tabbed SPA returns active_tab string; navigation breadcrumb includes top nav + page H1.

---

## Issue #8 — selector failure diagnostics

GitHub issue felixgeelhaar/scout#8. wait_for / click on missing selector returns bare timeout. Need diagnostic context. On selector-not-found error from Click/WaitFor (and friends), return structured payload: {error:"selector_not_found", selector, matched:0, similar:[{selector,text,score}], page_title, page_h1}. Reuse existing suggestSelectorsInternal (agent/selector.go) to populate similar[]. Wrap MCP error so message contains formatted suggestions while structured field is reachable in MCP error data. Acceptance: clicking nonexistent selector with near-miss element returns at least one similar suggestion; existing tests still pass.

---

## Issue #9 — console_errors include network 4xx/5xx

GitHub issue felixgeelhaar/scout#9. console_errors only surfaces console.error/warn. Network 4xx/5xx are invisible. Extend ConsoleErrors response with network_failures: [{url, method, status, status_text, response_body_snippet, timestamp}]. Reuse existing network capture infra — auto-enable lightweight failure-only capture when console_errors is requested (no body for non-failure requests). Add new failed_requests MCP tool for explicit access. Acceptance: registration form submitting against backend returning 400 surfaces the failed request in console_errors.network_failures with URL + status + body snippet.

---

## Issue #10 — click_text shortcut tool

GitHub issue felixgeelhaar/scout#10. Add click_text({text, role?}) MCP tool + agent.Session.ClickText. Resolution order: aria-label exact match → button/a/role=button visible text match → closest interactive ancestor of matching text node. Optional role disambiguator (button vs link). Returns matched selector + element bounding box. Acceptance: click_text("Record Weight") clicks correct button on test fixture without prior observe call; ambiguity returns structured error with candidate list. Should work alongside existing select_by_prompt but cheaper (no fuzzy matching, exact visible text).

---

## Issue #11 — cookie management tools

GitHub issue felixgeelhaar/scout#11. Stale cookies survive backend restart and break subsequent flows silently. Add three MCP tools: cookies_list (returns [{name,domain,has_value,expires}]), cookies_clear (drops all cookies for current page domain or all if scope=all), cookies_set (set a cookie). Also add cookies_summary into observe() output: count + names only. Implement via CDP Network.getCookies / Network.clearBrowserCookies / Network.setCookie. Acceptance: cookies_clear after stale cookie state lets fresh login succeed; cookies_list shows session cookies with redacted values.

---

## Issue #12 — fill_form_semantic post-mutation state echo

GitHub issue felixgeelhaar/scout#12. fill_form_semantic returns success based only on whether DOM value was set, not whether framework reactive binding (Vue v-model / React onChange) updated. After setting value + dispatching input/change events, re-read the value and echo {value_set, value_observed, framework_reactive:bool, warning?}. If observed != set, set warning string. Same instrumentation for click on input[type=checkbox|radio]. Acceptance: setting field that has missing onInput handler returns warning; healthy v-model field returns matching set/observed; tests cover both paths.

---
