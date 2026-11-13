package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"time"

	stagehand "github.com/browserbase/stagehand/packages/sdk-go/v4"
)

type report struct {
	Product             string `json:"product"`
	SDKVersion          string `json:"sdk_version"`
	GoVersion           string `json:"go_version"`
	BrowserMode         string `json:"browser_mode"`
	ModelClientCreated  bool   `json:"model_client_created"`
	ModelAPIKeyRequired bool   `json:"model_api_key_required_for_this_path"`
	Success             bool   `json:"success"`
	VerifiedText        string `json:"verified_text"`
	TimeToVerifiedMS    int64  `json:"time_to_verified_ms"`
	Boundary            string `json:"measurement_boundary"`
}

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	started := time.Now()
	result, err := run(ctx, started)
	if err != nil {
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"success": false, "error": err.Error()})
		os.Exit(1)
	}
	if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
		fmt.Fprintln(os.Stderr, "could not encode report:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, started time.Time) (result report, retErr error) {
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprint(w, `<!doctype html><html><body><button id="complete" onclick="document.querySelector('#status').textContent='all systems go'">Complete task</button><p id="status">waiting</p></body></html>`)
	}))
	defer fixture.Close()

	browser, err := stagehand.LaunchLocalBrowser(ctx, &stagehand.LocalBrowserLaunchOptions{Headless: true})
	if err != nil {
		return report{}, fmt.Errorf("launch local browser: %w", err)
	}
	defer func() { retErr = errors.Join(retErr, browser.Close(ctx)) }()

	client, err := stagehand.Create(ctx, stagehand.CreateOptions{Browser: browser})
	if err != nil {
		return report{}, fmt.Errorf("initialize Stagehand worker without a model: %w", err)
	}
	defer func() { retErr = errors.Join(retErr, client.Close(ctx)) }()

	browserContext, err := client.Browser().Context()
	if err != nil {
		return report{}, fmt.Errorf("get browser context: %w", err)
	}
	page, err := browserContext.NewPage(ctx)
	if err != nil {
		return report{}, fmt.Errorf("create page: %w", err)
	}
	if _, err := page.Goto(ctx, fixture.URL, nil); err != nil {
		return report{}, fmt.Errorf("navigate to fixture: %w", err)
	}
	if err := page.Locator("#complete").Click(ctx, nil); err != nil {
		return report{}, fmt.Errorf("click completion button: %w", err)
	}
	text, err := page.Locator("#status").InnerText(ctx)
	if err != nil {
		return report{}, fmt.Errorf("read fixture postcondition: %w", err)
	}
	if text != "all systems go" {
		return report{}, fmt.Errorf("postcondition = %q, want %q", text, "all systems go")
	}
	return report{
		Product:             "stagehand-go-direct-page-api",
		SDKVersion:          "v4.3.0",
		GoVersion:           runtime.Version(),
		BrowserMode:         "local Chrome launched by Stagehand SDK",
		ModelClientCreated:  false,
		ModelAPIKeyRequired: false,
		Success:             true,
		VerifiedText:        text,
		TimeToVerifiedMS:    time.Since(started).Milliseconds(),
		Boundary:            "process start through local Chrome launch, Stagehand worker initialization without a model, fixture navigation, click, and direct DOM postcondition read; excludes Go module download and Chrome installation",
	}, nil
}
