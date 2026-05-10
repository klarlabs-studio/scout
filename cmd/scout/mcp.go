package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/felixgeelhaar/mcp-go"

	browse "github.com/felixgeelhaar/scout"
	"github.com/felixgeelhaar/scout/agent"
)

type MCPErrorEnvelope struct {
	Code       string `json:"code"`
	Message    string `json:"message"`
	Phase      string `json:"phase,omitempty"`
	Cause      string `json:"cause,omitempty"`
	StatusCode int    `json:"status_code,omitempty"`
	URL        string `json:"url,omitempty"`
	Detail     string `json:"detail,omitempty"`
	Hint       string `json:"hint,omitempty"`
}

func envBool(key string) bool {
	v := os.Getenv(key)
	if v == "" {
		return false
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false
	}
	return b
}

func mcpErr(err error) error {
	if err == nil {
		return nil
	}
	var opErr *agent.OperationError
	if errors.As(err, &opErr) {
		env := MCPErrorEnvelope{
			Code:       "SCOUT_OPERATION_ERROR",
			Message:    opErr.OriginalError,
			Phase:      opErr.Phase,
			Cause:      opErr.Cause,
			StatusCode: opErr.StatusCode,
			URL:        opErr.URL,
			Detail:     opErr.Detail,
		}
		switch opErr.Cause {
		case "timeout":
			env.Hint = "Try reset, then retry the action."
		case "connection_refused", "connection_error":
			env.Hint = "Check browser/CDP availability and target URL reachability."
		case "http_401", "http_403":
			env.Hint = "Authentication or authorization required."
		case "http_404":
			env.Hint = "The target URL or resource was not found."
		case "browser_closed":
			env.Hint = "Browser was closed; call reset and retry."
		}
		b, _ := json.Marshal(env)
		return fmt.Errorf("SCOUT_ERROR %s", string(b))
	}
	return err
}

// --- Tool input types ---

type NavigateInput struct {
	URL string `json:"url" jsonschema:"required,description=URL to navigate to"`
}

type ClickInput struct {
	Selector string `json:"selector" jsonschema:"required,description=CSS selector of element to click"`
	Wait     bool   `json:"wait,omitempty" jsonschema:"description=If true wait for full page navigation after click"`
}

type TypeInput struct {
	Selector string `json:"selector" jsonschema:"required,description=CSS selector of input element"`
	Text     string `json:"text" jsonschema:"required,description=Text to type into the element"`
}

type FillFormInput struct {
	Fields map[string]string `json:"fields" jsonschema:"required,description=Field values keyed by CSS selector"`
}

type ExtractInput struct {
	Selector string `json:"selector" jsonschema:"required,description=CSS selector to extract text from"`
}

type ExtractAllInput struct {
	Selector string `json:"selector" jsonschema:"required,description=CSS selector to extract all matching texts"`
}

type ExtractTableInput struct {
	Selector string `json:"selector" jsonschema:"required,description=CSS selector for the table element"`
}

type ScreenshotInput struct {
	URL      string `json:"url,omitempty" jsonschema:"description=Optional URL to navigate to before taking screenshot"`
	Quality  int    `json:"quality,omitempty" jsonschema:"description=JPEG quality 1-100. Forces JPEG format. Lower = smaller file. Default auto-compresses to fit 200KB."`
	MaxWidth int    `json:"max_width,omitempty" jsonschema:"description=Maximum image width in pixels. Downscales proportionally. Good values: 800 or 1024."`
	FullPage bool   `json:"full_page,omitempty" jsonschema:"description=Capture the entire scrollable page instead of just the viewport."`
}

type HasElementInput struct {
	Selector string `json:"selector" jsonschema:"required,description=CSS selector to check for"`
}

type WaitForInput struct {
	Selector string `json:"selector" jsonschema:"required,description=CSS selector to wait for"`
}

type ObserveInput struct {
	URL string `json:"url,omitempty" jsonschema:"description=Optional URL to navigate to before observing"`
}

type ObserveWithBudgetInput struct {
	Budget int `json:"budget" jsonschema:"required,description=Approximate token budget for the response"`
}

type PDFInput struct{}

type DiscoverFormInput struct {
	Selector string `json:"selector,omitempty" jsonschema:"description=CSS selector for specific form (empty = all forms)"`
}

type FillFormSemanticInput struct {
	Fields map[string]any `json:"fields" jsonschema:"required,description=Field values keyed by human-readable field name. Strings fill text inputs/textareas/selects. Booleans toggle checkboxes (or pick a radio when paired with a string label)."`
}

type EnableNetworkInput struct {
	Patterns []string `json:"patterns,omitempty" jsonschema:"description=URL substring patterns to capture (empty = all)"`
}

type NetworkRequestsInput struct {
	Pattern   string `json:"pattern,omitempty" jsonschema:"description=URL substring filter"`
	MaxRecent int    `json:"max_recent,omitempty" jsonschema:"description=Maximum number of most recent requests to return (0 = all)"`
}

type AnnotatedScreenshotInput struct {
	IncludeImage bool `json:"include_image,omitempty" jsonschema:"description=Include base64 image data in response. Default false to avoid large responses. Use screenshot tool separately if you need the image."`
}

type AnnotatedScreenshotResult struct {
	Image    string                   `json:"image,omitempty"`
	Elements []agent.AnnotatedElement `json:"elements"`
	Count    int                      `json:"count"`
}

type ClickLabelInput struct {
	Label json.Number `json:"label" jsonschema:"required,description=Label number from annotated screenshot (e.g. 8)"`
}

type ComponentStateInput struct {
	Selector string `json:"selector" jsonschema:"required,description=CSS selector of the component root element"`
}

type DispatchEventInput struct {
	Selector  string         `json:"selector" jsonschema:"required,description=CSS selector of the target element"`
	EventType string         `json:"event_type" jsonschema:"required,description=DOM event type (e.g. click, input, custom-event)"`
	Detail    map[string]any `json:"detail,omitempty" jsonschema:"description=Event detail/payload data"`
}

type ConfigureInput struct {
	Headless        bool `json:"headless" jsonschema:"description=Run browser in headless mode (no visible window). Default true."`
	AllowPrivateIPs bool `json:"allow_private_ips,omitempty" jsonschema:"description=Allow navigation to loopback (127.0.0.1, localhost) and private IPs (10.*, 192.168.*, etc). Required for local-dev workflows. Default false."`
}

type ResetInput struct{}

type StatusInput struct{}

type HoverInput struct {
	Selector string `json:"selector" jsonschema:"required,description=CSS selector of element to hover over"`
}

type SelectOptionInput struct {
	Selector string `json:"selector" jsonschema:"required,description=CSS selector of the select element"`
	Option   string `json:"option" jsonschema:"required,description=Option text or value to select"`
}

type ScrollToInput struct {
	Selector string `json:"selector" jsonschema:"required,description=CSS selector of element to scroll into view"`
}

type ScrollByInput struct {
	X int `json:"x" jsonschema:"description=Horizontal scroll offset in pixels"`
	Y int `json:"y" jsonschema:"required,description=Vertical scroll offset in pixels (positive=down)"`
}

type DragDropInput struct {
	From string `json:"from" jsonschema:"required,description=CSS selector of element to drag"`
	To   string `json:"to" jsonschema:"required,description=CSS selector of drop target"`
}

type FocusInput struct {
	Selector string `json:"selector" jsonschema:"required,description=CSS selector of element to focus"`
}

type StartRecordInput struct {
	Name string `json:"name" jsonschema:"required,description=Name for the playbook being recorded"`
}

type SavePlaybookInput struct {
	Path string `json:"path" jsonschema:"required,description=File path to save the playbook JSON"`
}

type ReplayInput struct {
	Path string `json:"path" jsonschema:"required,description=File path to the playbook JSON file"`
}

type StartScreenRecordingInput struct {
	Width     int    `json:"width,omitempty" jsonschema:"description=Capture width in CSS pixels (default 1280)"`
	Height    int    `json:"height,omitempty" jsonschema:"description=Capture height in CSS pixels (default 800)"`
	FPS       int    `json:"fps,omitempty" jsonschema:"description=Target frames per second 1-60 (default 30)"`
	Quality   int    `json:"quality,omitempty" jsonschema:"description=JPEG frame quality 1-100 (default 80)"`
	Format    string `json:"format,omitempty" jsonschema:"description=Output format: webm (default if ffmpeg present), mp4, or frames"`
	OutputDir string `json:"output_dir,omitempty" jsonschema:"description=Parent directory for output. Defaults to OS temp dir."`
}

type StopScreenRecordingInput struct{}

type TabInput struct {
	Name string `json:"name,omitempty" jsonschema:"description=Tab name (for open_tab and switch_tab)"`
	URL  string `json:"url,omitempty" jsonschema:"description=URL to open in new tab"`
}

type HistoryInput struct {
	Count int `json:"count,omitempty" jsonschema:"description=Number of recent actions to return (default 5, max 20)"`
}

type SuggestInput struct {
	Selector string `json:"selector" jsonschema:"required,description=The selector that failed to match"`
}

type SelectByPromptInput struct {
	Prompt string `json:"prompt" jsonschema:"required,description=Natural language description of the element to find (e.g. 'the login button' or 'search input')"`
}

type SwitchToFrameInput struct {
	Selector string `json:"selector" jsonschema:"required,description=CSS selector of the iframe element to switch into"`
}

type StopTraceInput struct {
	Path string `json:"path" jsonschema:"required,description=File path to write the trace zip file"`
}

type HybridObserveInput struct {
	IncludeImage bool `json:"include_image,omitempty" jsonschema:"description=Include base64 screenshot in response. Default false to keep responses compact."`
}

type HybridObserveResult struct {
	Image    string                `json:"image,omitempty"`
	Elements []agent.HybridElement `json:"elements"`
	Width    int                   `json:"viewport_width"`
	Height   int                   `json:"viewport_height"`
}

type FindByCoordinatesInput struct {
	X int `json:"x" jsonschema:"required,description=X pixel coordinate in the viewport"`
	Y int `json:"y" jsonschema:"required,description=Y pixel coordinate in the viewport"`
}

type BatchInput struct {
	Actions []agent.BatchAction `json:"actions" jsonschema:"required,description=Array of actions to execute. Each has action (click/type/fill_form_semantic/wait/scroll_to/click_label) plus selector/value/label/fields as needed."`
}

type ObserveDiffResult struct {
	Observation *agent.Observation `json:"observation"`
	Diff        *agent.DOMDiff     `json:"diff"`
}

func serveMCP() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		cancel()
	}()

	// Lazy session — created on first tool use, can be reconfigured without restart.
	// All handlers reference `session` which is lazily initialized via ensureSession().
	var (
		session    *agent.Session
		sessionCfg = agent.SessionConfig{
			Headless:        true,
			AllowPrivateIPs: envBool("SCOUT_ALLOW_PRIVATE_IPS"),
		}
		sessionMu sync.Mutex
	)

	ensureSession := func() error {
		sessionMu.Lock()
		defer sessionMu.Unlock()
		if session != nil {
			return nil
		}
		s, err := agent.NewSession(sessionCfg)
		if err != nil {
			return err
		}
		session = s
		return nil
	}

	reconfigure := func(cfg agent.SessionConfig) error {
		sessionMu.Lock()
		defer sessionMu.Unlock()
		if session != nil {
			// Close in goroutine with timeout — don't block if CDP calls are in flight
			old := session
			session = nil
			go func() {
				done := make(chan struct{})
				go func() { _ = old.Close(); close(done) }()
				select {
				case <-done:
				case <-time.After(3 * time.Second):
					// Force abandon — the browser process will be cleaned up by OS
				}
			}()
		}
		sessionCfg = cfg
		return nil
	}

	defer func() {
		sessionMu.Lock()
		if session != nil {
			_ = session.Close()
		}
		sessionMu.Unlock()
	}()

	srv := mcp.NewServer(mcp.ServerInfo{
		Name:    "scout",
		Version: version,
		Capabilities: mcp.Capabilities{
			Tools: true,
		},
	}, mcp.WithInstructions(`Scout provides browser automation tools for navigating websites,
filling forms, extracting data, and taking screenshots. Start with 'navigate' to load a page,
then use 'observe' to see interactive elements, and perform actions with 'click', 'type',
'fill_form_semantic', 'extract', 'extract_table', etc. Use 'observe_diff' after actions to
see only what changed. Use 'annotated_screenshot' for visual element identification.
Use 'configure' to switch between headless and visible browser modes without restarting.

IMPORTANT: Scout uses standard CSS selectors, NOT Playwright selectors. Do NOT use :text(), :has-text(), >> chaining, or other Playwright-specific syntax. Instead:
- To find by text content: use 'observe' or 'annotated_screenshot' to discover elements, then click by selector or label number
- To find a button by text: use 'fill_form_semantic' for forms, or call 'annotated_screenshot' and use 'click_label' with the label number
- Valid selectors: #id, .class, tag, [attr=value], tag:nth-of-type(n), tag:first-child, etc.

IMPORTANT: fill_form and fill_form_semantic take a JSON OBJECT (not array) for fields:
  fill_form: {"fields": {"#email": "value", "#password": "value"}}
  fill_form_semantic: {"fields": {"Email": "value", "Password": "value"}}
Do NOT send fields as an array of objects.

WORKFLOW: navigate first, then use other tools. Use 'dismiss_cookies' after navigate if a cookie banner appears. Use 'check_readiness' if the page seems to still be loading.`))

	// s returns the current session, lazily creating it on first use.
	// Every handler calls this instead of accessing session directly.
	s := func() *agent.Session {
		if err := ensureSession(); err != nil {
			panic(fmt.Sprintf("failed to create browser session: %v", err))
		}
		return session
	}

	// maybeNavigate navigates if a URL is provided, otherwise uses the current page.
	maybeNavigate := func(url string) error {
		if url != "" {
			_, err := s().Navigate(url)
			return mcpErr(err)
		}
		return nil
	}

	// --- Configuration ---

	srv.Tool("configure").
		ClosedWorld().Idempotent().
		Description("Change browser settings without restarting. Use headless=false to see the browser window. Set allow_private_ips=true for local-dev workflows (localhost, 127.0.0.1, private IPs).").
		Handler(func(ctx context.Context, input ConfigureInput) (string, error) {
			if err := reconfigure(agent.SessionConfig{
				Headless:        input.Headless,
				AllowPrivateIPs: input.AllowPrivateIPs,
			}); err != nil {
				return "", err
			}
			mode := "headless"
			if !input.Headless {
				mode = "visible"
			}
			privIPs := "blocked"
			if input.AllowPrivateIPs {
				privIPs = "allowed"
			}
			return fmt.Sprintf("Browser reconfigured: %s mode, private IPs %s. Next navigation will use the new settings.", mode, privIPs), nil
		})

	srv.Tool("reset").
		ClosedWorld().
		Description("Force-reset the current browser session, clearing stuck state and creating a fresh page context.").
		Handler(func(ctx context.Context, input ResetInput) (string, error) {
			if err := s().Reset(); err != nil {
				return "", err
			}
			return "Session reset completed", nil
		})

	srv.Tool("status").
		ReadOnly().
		Description("Get current browser/session health status including URL, pending requests, timeout streak, and last error.").
		Handler(func(ctx context.Context, input StatusInput) (*agent.SessionStatus, error) {
			return s().Status(), nil
		})

		// --- Navigation & Observation ---

	srv.Tool("navigate").
		OpenWorld().
		Description("Navigate to a URL. Returns page title and URL.").
		Handler(func(ctx context.Context, input NavigateInput) (*agent.PageResult, error) {
			progress := mcp.ProgressFromContext(ctx)
			total := 3.0
			_ = progress.ReportWithMessage(1, &total, "Launching browser...")
			result, err := s().Navigate(input.URL)
			if err != nil {
				return nil, mcpErr(err)
			}
			_ = progress.ReportWithMessage(2, &total, "Page loaded")
			_ = progress.ReportWithMessage(3, &total, "Done")
			// Notify client that available tools may have changed based on page content
			if sess := mcp.SessionFromContext(ctx); sess != nil {
				_ = sess.NotifyToolListChanged()
			}
			// Push page info via channel if supported
			if ch := mcp.ChannelFromContext(ctx); ch != nil {
				_ = ch.SendText("scout.navigation", fmt.Sprintf("Navigated to %s — %s", result.URL, result.Title))
			}
			return result, nil
		})

	srv.Tool("observe").
		ReadOnly().
		OutputSchema(agent.Observation{}).
		Description("Get a structured snapshot of the current page. Optionally pass url to navigate first.").
		Handler(func(ctx context.Context, input ObserveInput) (*agent.Observation, error) {
			if err := maybeNavigate(input.URL); err != nil {
				return nil, err
			}
			return s().Observe()
		})

	srv.Tool("observe_diff").
		ReadOnly().
		Description("Return only page changes since the last observation.").
		Handler(func(ctx context.Context, input ObserveInput) (*ObserveDiffResult, error) {
			obs, diff, err := s().ObserveDiff()
			if err != nil {
				return nil, err
			}
			return &ObserveDiffResult{Observation: obs, Diff: diff}, nil
		})

	srv.Tool("observe_with_budget").
		ReadOnly().
		Description("Observe the page within a token budget.").
		Handler(func(ctx context.Context, input ObserveWithBudgetInput) (*agent.Observation, error) {
			return s().ObserveWithBudget(input.Budget)
		})

		// --- Interaction ---

	srv.Tool("click").
		Description("Click an element by CSS selector. Set wait=true for navigation clicks.").
		Handler(func(ctx context.Context, input ClickInput) (*agent.PageResult, error) {
			if input.Wait {
				out, err := s().ClickAndWait(input.Selector)
				return out, mcpErr(err)
			}
			out, err := s().Click(input.Selector)
			return out, mcpErr(err)
		})

	srv.Tool("click_label").
		Description("Click an element by its label number from annotated_screenshot.").
		Handler(func(ctx context.Context, input ClickLabelInput) (*agent.PageResult, error) {
			label, err := strconv.Atoi(input.Label.String())
			if err != nil {
				return nil, fmt.Errorf("label must be a number (e.g. 8), got %q", input.Label.String())
			}
			return s().ClickLabel(label)
		})

	srv.Tool("click_text").
		Description("Click an element by its visible text. Resolution: aria-label exact match, then button/link text match, then closest interactive ancestor of a matching text node. Pass role=\"button\"|\"link\" to disambiguate when text appears in both. Cheaper than annotated_screenshot for the common 'click that thing I can see' case.").
		Handler(func(ctx context.Context, input struct {
			Text string `json:"text" jsonschema:"required,description=Visible text to match (exact, case-insensitive)."`
			Role string `json:"role,omitempty" jsonschema:"description=Optional role filter: button or link."`
		}) (*agent.ClickTextResult, error) {
			out, err := s().ClickText(input.Text, input.Role)
			return out, mcpErr(err)
		})

	srv.Tool("type").
		Description("Type text into an input element. Clears existing value first.").
		Handler(func(ctx context.Context, input TypeInput) (*agent.ElementResult, error) {
			out, err := s().Type(input.Selector, input.Text)
			return out, mcpErr(err)
		})

	srv.Tool("fill_form").
		Description("Fill multiple form fields at once.").
		Handler(func(ctx context.Context, input FillFormInput) (*agent.FormResult, error) {
			out, err := s().FillForm(input.Fields)
			return out, mcpErr(err)
		})

	srv.Tool("fill_form_semantic").
		Description("Fill form fields by label or name. Strings fill text inputs; booleans toggle checkboxes; for radios pass the label/value as a string. Each result includes the value re-read after dispatching input/change events plus a warning when framework binding (Vue v-model / React onChange) didn't pick up the change.").
		Handler(func(ctx context.Context, input FillFormSemanticInput) (*agent.SemanticFillResult, error) {
			out, err := s().FillFormSemanticAny(input.Fields)
			return out, mcpErr(err)
		})

	srv.Tool("dispatch_event").
		Description("Dispatch a DOM event on an element. Useful for triggering SPA event handlers.").
		Handler(func(ctx context.Context, input DispatchEventInput) (string, error) {
			if err := s().DispatchEvent(input.Selector, input.EventType, input.Detail); err != nil {
				return "", err
			}
			return fmt.Sprintf("Dispatched %s on %s", input.EventType, input.Selector), nil
		})

	srv.Tool("hover").
		Description("Hover over an element to trigger CSS :hover states, tooltips, and dropdown menus.").
		Handler(func(ctx context.Context, input HoverInput) (*agent.PageResult, error) {
			return s().Hover(input.Selector)
		})

	srv.Tool("double_click").
		Description("Double-click an element.").
		Handler(func(ctx context.Context, input ClickInput) (*agent.PageResult, error) {
			return s().DoubleClick(input.Selector)
		})

	srv.Tool("right_click").
		Description("Right-click an element to trigger context menus.").
		Handler(func(ctx context.Context, input ClickInput) (*agent.PageResult, error) {
			return s().RightClick(input.Selector)
		})

	srv.Tool("select_option").
		Description("Select an option from a dropdown/select element by visible text or value.").
		Handler(func(ctx context.Context, input SelectOptionInput) (*agent.ElementResult, error) {
			return s().SelectOption(input.Selector, input.Option)
		})

	srv.Tool("scroll_to").
		Description("Scroll to bring an element into view.").
		Handler(func(ctx context.Context, input ScrollToInput) (*agent.PageResult, error) {
			return s().ScrollTo(input.Selector)
		})

	srv.Tool("scroll_by").
		Description("Scroll the page by pixel offset. Positive y = scroll down.").
		Handler(func(ctx context.Context, input ScrollByInput) (*agent.PageResult, error) {
			return s().ScrollBy(input.X, input.Y)
		})

	srv.Tool("focus").
		Description("Set focus on an element, triggering :focus CSS state.").
		Handler(func(ctx context.Context, input FocusInput) (*agent.PageResult, error) {
			return s().Focus(input.Selector)
		})

	srv.Tool("drag_drop").
		Description("Drag an element and drop it on another element.").
		Handler(func(ctx context.Context, input DragDropInput) (*agent.PageResult, error) {
			return s().DragDrop(input.From, input.To)
		})

	// --- Batch ---

	srv.Tool("batch").
		Description("Execute multiple actions in a single call. Avoids repeated round-trips. Actions: click, type, fill_form_semantic, wait, scroll_to, click_label. Continues on error.").
		Handler(func(ctx context.Context, input BatchInput) (*agent.BatchResult, error) {
			return s().ExecuteBatch(input.Actions)
		})

		// --- Extraction ---

	srv.Tool("extract").
		ReadOnly().
		Description("Extract text content from a single element.").
		Handler(func(ctx context.Context, input ExtractInput) (*agent.ElementResult, error) {
			out, err := s().Extract(input.Selector)
			return out, mcpErr(err)
		})

	srv.Tool("extract_all").
		ReadOnly().
		Description("Extract text from all elements matching a selector.").
		Handler(func(ctx context.Context, input ExtractAllInput) (*agent.ExtractAllResult, error) {
			return s().ExtractAll(input.Selector)
		})

	srv.Tool("extract_table").
		ReadOnly().
		Description("Extract structured data from an HTML table (headers + rows).").
		Handler(func(ctx context.Context, input ExtractTableInput) (*agent.TableResult, error) {
			return s().ExtractTable(input.Selector)
		})

	srv.Tool("markdown").
		ReadOnly().
		Description("Get a compact markdown representation of the page. Ideal for LLM processing.").
		Handler(func(ctx context.Context, input ObserveInput) (string, error) {
			return s().Markdown()
		})

	srv.Tool("readable_text").
		ReadOnly().
		Description("Extract just the main readable content, stripping navigation and boilerplate.").
		Handler(func(ctx context.Context, input ObserveInput) (string, error) {
			return s().ReadableText()
		})

	srv.Tool("accessibility_tree").
		ReadOnly().
		Description("Get a compact accessibility tree showing all interactive elements.").
		Handler(func(ctx context.Context, input ObserveInput) (string, error) {
			return s().AccessibilityTree()
		})

	// --- Capture ---

	srv.Tool("screenshot").
		ReadOnly().
		Description("Capture a screenshot. Defaults to JPEG quality 60 with 80KB cap (~20k tokens base64) so result fits MCP tool-result token limits. Pass quality to override; on overflow the image is progressively downscaled rather than failing. Returns base64 data URL.").
		Handler(func(ctx context.Context, input ScreenshotInput) (string, error) {
			if err := maybeNavigate(input.URL); err != nil {
				return "", err
			}
			page := s().Page()
			if page == nil {
				return "", fmt.Errorf("no page open")
			}
			opts := browse.ScreenshotOptions{
				MaxSize:  80 * 1024, // 80KB default — fits comfortably under MCP per-result token caps
				FullPage: input.FullPage,
				MaxWidth: input.MaxWidth,
				Format:   "jpeg",
				Quality:  60,
			}
			if input.Quality > 0 {
				opts.Quality = input.Quality
			}
			data, err := page.ScreenshotWithOptions(opts)
			if err != nil {
				return "", err
			}
			mime := "image/jpeg"
			if opts.Format == "png" {
				mime = "image/png"
			}
			return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data), nil
		})

	srv.Tool("annotated_screenshot").
		ReadOnly().
		Description("Label all interactive elements with numbers and return their selectors/info. By default returns only the element list (compact). Set include_image=true to also get the screenshot with labels drawn on it.").
		Handler(func(ctx context.Context, input AnnotatedScreenshotInput) (*AnnotatedScreenshotResult, error) {
			result, err := s().AnnotatedScreenshot()
			if err != nil {
				return nil, err
			}
			out := &AnnotatedScreenshotResult{
				Elements: result.Elements,
				Count:    result.Count,
			}
			if input.IncludeImage {
				out.Image = "data:image/png;base64," + base64.StdEncoding.EncodeToString(result.Image)
			}
			return out, nil
		})

	srv.Tool("pdf").
		ReadOnly().
		Description("Generate a PDF of the current page.").
		Handler(func(ctx context.Context, input PDFInput) (string, error) {
			data, err := s().PDF()
			if err != nil {
				return "", err
			}
			return "data:application/pdf;base64," + base64.StdEncoding.EncodeToString(data), nil
		})

	// --- Network ---

	srv.Tool("enable_network_capture").
		ClosedWorld().
		Description("Start capturing network XHR/fetch responses. Patterns filter by URL substring.").
		Handler(func(ctx context.Context, input EnableNetworkInput) (string, error) {
			if err := s().EnableNetworkCapture(input.Patterns...); err != nil {
				return "", err
			}
			return "Network capture enabled", nil
		})

	srv.Tool("network_requests").
		ReadOnly().
		Description("Get captured network requests/responses including request bodies (POST/PUT/PATCH) and response bodies (max 32KB each, truncated if larger). Includes recent buffered requests so you can inspect traffic even if capture was enabled late.").
		Handler(func(ctx context.Context, input NetworkRequestsInput) ([]agent.NetworkCapture, error) {
			out := s().CapturedRequests(input.Pattern)
			if input.MaxRecent > 0 && len(out) > input.MaxRecent {
				out = out[len(out)-input.MaxRecent:]
			}
			return out, nil
		})

	// --- Framework support ---

	srv.Tool("wait_spa").
		ReadOnly().
		Description("Wait for SPA framework (React/Vue/Angular/Next.js/Svelte) to finish rendering.").
		Handler(func(ctx context.Context, input ObserveInput) (*agent.PageResult, error) {
			if err := s().WaitForSPA(); err != nil {
				return nil, err
			}
			return s().Snapshot()
		})

	srv.Tool("detect_frameworks").
		ReadOnly().
		Description("Detect which frontend frameworks are active on the page.").
		Handler(func(ctx context.Context, input ObserveInput) ([]string, error) {
			return s().DetectedFrameworks()
		})

	srv.Tool("component_state").
		ReadOnly().
		Description("Extract component state/props from any framework (React, Vue, Svelte, Angular, Alpine, Lit).").
		Handler(func(ctx context.Context, input ComponentStateInput) (map[string]any, error) {
			return s().ComponentState(input.Selector)
		})

	srv.Tool("app_state").
		ReadOnly().
		Description("Extract global app state (Redux, Next.js, Nuxt, Remix, SvelteKit, Gatsby, Astro, Alpine, HTMX).").
		Handler(func(ctx context.Context, input ObserveInput) (map[string]any, error) {
			return s().GetAppState()
		})

	// --- Dialog detection ---

	srv.Tool("detect_dialog").
		ReadOnly().
		Description("Check if a modal, dialog, popup, or overlay is currently visible. Returns its title, text, buttons, and inputs.").
		Handler(func(ctx context.Context, input ObserveInput) (*agent.DialogInfo, error) {
			return s().DetectDialog()
		})

	// --- Smart helpers ---

	srv.Tool("dismiss_cookies").
		Description("Auto-dismiss cookie consent banners. Tries common selectors and text patterns (Accept, Agree, Got it, OK). Returns whether a banner was found and dismissed.").
		Handler(func(ctx context.Context, input ObserveInput) (*agent.CookieDismissResult, error) {
			return s().DismissCookieBanner()
		})

	srv.Tool("cookies_list").
		ReadOnly().
		Description("List all cookies for the active page (names + flags only — values are redacted). Useful for diagnosing stale-session issues after a backend restart.").
		Handler(func(ctx context.Context, input ObserveInput) ([]agent.CookieInfo, error) {
			return s().ListCookies()
		})

	srv.Tool("cookies_clear").
		Description("Clear cookies. With no name, drops all cookies via Network.clearBrowserCookies. With a name, deletes only that cookie (optionally scoped by domain/path). Use this when a backend has been restarted and the previous session token is invalid.").
		Handler(func(ctx context.Context, input struct {
			Name   string `json:"name,omitempty" jsonschema:"description=Cookie name to delete. Leave empty to clear all cookies."`
			Domain string `json:"domain,omitempty" jsonschema:"description=Optional domain scope when deleting a single cookie."`
			Path   string `json:"path,omitempty" jsonschema:"description=Optional path scope when deleting a single cookie."`
		}) (map[string]any, error) {
			n, err := s().ClearCookies(input.Name, input.Domain, input.Path)
			if err != nil {
				return nil, err
			}
			return map[string]any{"removed": n}, nil
		})

	srv.Tool("cookies_set").
		Description("Set a cookie on the active page. Useful for restoring sessions or seeding state.").
		Handler(func(ctx context.Context, input agent.CookieInput) (string, error) {
			if err := s().SetCookie(input); err != nil {
				return "", err
			}
			return "ok", nil
		})

	srv.Tool("check_readiness").
		ReadOnly().
		Description("Check how ready the page is for interaction. Returns a 0-100 score, pending XHR count, skeleton/spinner presence, and suggestions for what to wait for.").
		Handler(func(ctx context.Context, input ObserveInput) (*agent.PageReadiness, error) {
			return s().CheckReadiness()
		})

	srv.Tool("web_vitals").
		ReadOnly().
		Description("Extract Core Web Vitals (LCP, CLS, INP) and performance timing (TTFB, First Paint, DOM Content Loaded). Each metric is rated good/needs-improvement/poor per Google thresholds.").
		Handler(func(ctx context.Context, input ObserveInput) (*agent.WebVitalsResult, error) {
			return s().WebVitals()
		})

	srv.Tool("select_by_prompt").
		ReadOnly().
		Description("Find an element using natural language (e.g. 'the login button', 'search input'). Returns the best match with confidence score and CSS selector.").
		Handler(func(ctx context.Context, input SelectByPromptInput) (*agent.PromptSelectResult, error) {
			return s().SelectByPrompt(input.Prompt)
		})

	srv.Tool("suggest_selectors").
		ReadOnly().
		Description("Find elements similar to a selector that failed. Returns up to 5 suggestions with selector, tag, text, and classes.").
		Handler(func(ctx context.Context, input SuggestInput) ([]agent.SelectorSuggestion, error) {
			return s().SuggestSelectors(input.Selector)
		})

	srv.Tool("hybrid_observe").
		ReadOnly().
		Description("Vision+DOM hybrid mode: returns a clean screenshot (no labels) plus bounding boxes for all interactive elements. Use find_by_coordinates to select elements by pixel position. Set include_image=true to get the base64 screenshot.").
		Handler(func(ctx context.Context, input HybridObserveInput) (*HybridObserveResult, error) {
			result, err := s().HybridObserve()
			if err != nil {
				return nil, err
			}
			out := &HybridObserveResult{
				Elements: result.Elements,
				Width:    result.Width,
				Height:   result.Height,
			}
			if input.IncludeImage {
				out.Image = "data:image/png;base64," + base64.StdEncoding.EncodeToString(result.Screenshot)
			}
			return out, nil
		})

	srv.Tool("find_by_coordinates").
		ReadOnly().
		Description("Find the interactive element at given pixel coordinates. Returns the smallest element containing that point, with CSS selector and text. Use after hybrid_observe.").
		Handler(func(ctx context.Context, input FindByCoordinatesInput) (*agent.PromptSelectResult, error) {
			return s().FindByCoordinates(input.X, input.Y)
		})

	srv.Tool("session_history").
		ReadOnly().
		Description("Get the last N actions performed in this session. Provides context about what has been done so far.").
		Handler(func(ctx context.Context, input HistoryInput) ([]agent.HistoryEntry, error) {
			count := input.Count
			if count == 0 {
				count = 5
			}
			return s().SessionHistory(count), nil
		})

	// --- Smart extraction ---

	srv.Tool("auto_extract").
		ReadOnly().
		Description("Auto-detect repeating patterns (product cards, search results, list items) and extract structured data. No selectors needed.").
		Handler(func(ctx context.Context, input ObserveInput) (*agent.ExtractedPattern, error) {
			return s().AutoExtract()
		})

	srv.Tool("scroll_and_collect").
		Description("Auto-scroll the page and collect items as they lazy-load. For infinite scroll pages.").
		Handler(func(ctx context.Context, input struct {
			Selector string `json:"selector" jsonschema:"required,description=CSS selector for the repeating items"`
			MaxItems int    `json:"max_items,omitempty" jsonschema:"description=Maximum items to collect (default 100)"`
		}) (*agent.ExtractAllResult, error) {
			return s().ScrollAndCollect(input.Selector, input.MaxItems)
		})

	// --- Diagnostics ---

	srv.Tool("console_errors").
		ReadOnly().
		Description("Get captured console.error / console.warn messages plus recent network 4xx/5xx failures. Auto-installs lightweight network observers, so failures recorded after the first call surface here without an explicit enable_network_capture.").
		Handler(func(ctx context.Context, input ObserveInput) (*agent.DiagnosticsResult, error) {
			return s().Diagnostics()
		})

	srv.Tool("failed_requests").
		ReadOnly().
		Description("Recent network requests with status >= 400 (4xx/5xx). URL + method + status + response body snippet. Use after a form submission that silently failed.").
		Handler(func(ctx context.Context, input ObserveInput) ([]agent.NetworkFailure, error) {
			return s().FailedRequests()
		})

	srv.Tool("detect_auth_wall").
		ReadOnly().
		Description("Check if the page is a login wall, paywall, or CAPTCHA. Returns type, confidence, and reason.").
		Handler(func(ctx context.Context, input ObserveInput) (*agent.AuthWallResult, error) {
			return s().DetectAuthWall()
		})

	srv.Tool("upload_file").
		Description("Upload a file to a file input element.").
		Handler(func(ctx context.Context, input struct {
			Selector string `json:"selector" jsonschema:"required,description=CSS selector of the file input element"`
			FilePath string `json:"file_path" jsonschema:"required,description=Local path to the file to upload"`
		}) (string, error) {
			if err := s().UploadFile(input.Selector, input.FilePath); err != nil {
				return "", err
			}
			return fmt.Sprintf("Uploaded %s to %s", input.FilePath, input.Selector), nil
		})

	srv.Tool("compare_tabs").
		ReadOnly().
		Description("Compare content between two named tabs. Returns what's different, what's only in one tab.").
		Handler(func(ctx context.Context, input struct {
			Tab1 string `json:"tab1" jsonschema:"required,description=Name of the first tab"`
			Tab2 string `json:"tab2" jsonschema:"required,description=Name of the second tab"`
		}) (*agent.PageDiff, error) {
			return s().CompareTabs(input.Tab1, input.Tab2)
		})

	// --- Utility ---

	srv.Tool("has_element").
		ReadOnly().
		Description("Check if an element exists on the page.").
		Handler(func(ctx context.Context, input HasElementInput) (bool, error) {
			return s().HasElement(input.Selector), nil
		})

	srv.Tool("wait_for").
		ReadOnly().
		Description("Wait for an element to appear in the DOM.").
		Handler(func(ctx context.Context, input WaitForInput) (*agent.PageResult, error) {
			if err := s().WaitFor(input.Selector); err != nil {
				return nil, mcpErr(err)
			}
			return s().Snapshot()
		})

	srv.Tool("discover_form").
		ReadOnly().
		Description("Discover form fields with their labels, types, and CSS selectors.").
		Handler(func(ctx context.Context, input DiscoverFormInput) (*agent.FormDiscoveryResult, error) {
			return s().DiscoverForm(input.Selector)
		})

	// --- Tabs ---

	srv.Tool("open_tab").
		OpenWorld().
		Description("Open a new named browser tab and navigate to a URL. The new tab becomes active.").
		Handler(func(ctx context.Context, input TabInput) (*agent.PageResult, error) {
			return s().OpenTab(input.Name, input.URL)
		})

	srv.Tool("switch_tab").
		ClosedWorld().
		Description("Switch to a named tab. Use list_tabs to see available tabs.").
		Handler(func(ctx context.Context, input TabInput) (*agent.PageResult, error) {
			return s().SwitchTab(input.Name)
		})

	srv.Tool("close_tab").
		ClosedWorld().
		Description("Close a named tab. Cannot close the currently active tab.").
		Handler(func(ctx context.Context, input TabInput) (string, error) {
			if err := s().CloseTab(input.Name); err != nil {
				return "", err
			}
			return fmt.Sprintf("Closed tab %q", input.Name), nil
		})

	srv.Tool("list_tabs").
		ReadOnly().
		Description("List all open tabs with their names, URLs, and titles.").
		Handler(func(ctx context.Context, input ObserveInput) ([]agent.TabInfo, error) {
			return s().ListTabs()
		})

	// --- Frames ---

	srv.Tool("switch_to_frame").
		ClosedWorld().
		Description("Switch execution context into an iframe. Subsequent actions operate inside the iframe until switch_to_main_frame is called.").
		Handler(func(ctx context.Context, input SwitchToFrameInput) (*agent.PageResult, error) {
			return s().SwitchToFrame(input.Selector)
		})

	srv.Tool("switch_to_main_frame").
		ClosedWorld().
		Description("Switch back to the main page frame after operating inside an iframe.").
		Handler(func(ctx context.Context, input ObserveInput) (*agent.PageResult, error) {
			return s().SwitchToMainFrame()
		})

	// --- Playbook ---

	srv.Tool("start_recording").
		ClosedWorld().
		Description("Start recording browser actions into a replayable playbook. Call stop_recording when done.").
		Handler(func(ctx context.Context, input StartRecordInput) (string, error) {
			s().StartRecordingPlaybook(input.Name)
			return fmt.Sprintf("Recording started: %s", input.Name), nil
		})

	srv.Tool("stop_recording").
		ClosedWorld().
		Description("Stop recording and return the playbook. Save it with save_playbook for later replay.").
		Handler(func(ctx context.Context, input ObserveInput) (*agent.Playbook, error) {
			return s().StopRecordingPlaybook()
		})

	srv.Tool("save_playbook").
		ClosedWorld().
		Description("Save the last recorded playbook to a JSON file for deterministic replay.").
		Handler(func(ctx context.Context, input SavePlaybookInput) (string, error) {
			pb, err := s().StopRecordingPlaybook()
			if err != nil {
				return "", err
			}
			if err := agent.SavePlaybook(pb, input.Path); err != nil {
				return "", err
			}
			return fmt.Sprintf("Playbook saved to %s (%d actions)", input.Path, len(pb.Actions)), nil
		})

	srv.Tool("replay_playbook").
		OpenWorld().
		Description("Replay a saved playbook deterministically without LLM calls. Returns success/failure and any extracted data.").
		Handler(func(ctx context.Context, input ReplayInput) (*agent.PlaybookResult, error) {
			pb, err := agent.LoadPlaybook(input.Path)
			if err != nil {
				return nil, err
			}
			return s().ReplayPlaybook(pb)
		})

	// --- Screen Recording (video) ---

	srv.Tool("start_screen_recording").
		ClosedWorld().
		Description("Start capturing the active page as a screencast video. Frames are streamed via CDP and (if ffmpeg is in PATH) encoded to webm/mp4 on stop. Otherwise raw JPEG frames + ffmpeg concat list are returned for offline encoding. Call stop_screen_recording to finish.").
		Handler(func(ctx context.Context, input StartScreenRecordingInput) (string, error) {
			err := s().StartScreenRecording(agent.ScreenRecordingOptions{
				Width:     input.Width,
				Height:    input.Height,
				FPS:       input.FPS,
				Quality:   input.Quality,
				Format:    input.Format,
				OutputDir: input.OutputDir,
			})
			if err != nil {
				return "", mcpErr(err)
			}
			return "Screen recording started. Call stop_screen_recording to finish and obtain the video path.", nil
		})

	srv.Tool("stop_screen_recording").
		ClosedWorld().
		Description("Stop the active screen recording. Returns a struct with the output file path (video or frames dir), format, encoder used, frame count, and duration.").
		Handler(func(ctx context.Context, input StopScreenRecordingInput) (*agent.ScreenRecordingResult, error) {
			res, err := s().StopScreenRecording()
			if err != nil {
				return nil, mcpErr(err)
			}
			return res, nil
		})

	// --- Tracing ---

	srv.Tool("start_trace").
		ClosedWorld().
		Description("Start tracing all browser actions with before/after screenshots. Call stop_trace to export a zip file.").
		Handler(func(ctx context.Context, input ObserveInput) (string, error) {
			if err := s().StartTrace(); err != nil {
				return "", err
			}
			return "Trace started. Actions will be recorded with screenshots.", nil
		})

	srv.Tool("stop_trace").
		ClosedWorld().
		Description("Stop tracing and export a zip file containing trace events, screenshots, and network requests.").
		Handler(func(ctx context.Context, input StopTraceInput) (*agent.TraceResult, error) {
			return s().StopTrace(input.Path)
		})

	if err := mcp.ServeStdio(ctx, srv); err != nil && err != context.Canceled {
		fmt.Fprintf(os.Stderr, "MCP server error: %v\n", err)
		os.Exit(1)
	}
}
