// Example human_handoff opens a headed browser so a person can complete a
// login, 2FA, consent, or other gated step before Scout verifies the result.
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"go.klarlabs.de/scout/agent"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	url := flag.String("url", "", "page to open for the human step (required)")
	expected := flag.String("expect", "", "text that must be visible after the human step (required)")
	flag.Parse()
	if strings.TrimSpace(*url) == "" || strings.TrimSpace(*expected) == "" {
		return fmt.Errorf("usage: go run ./examples/human_handoff -url https://example.com/login -expect 'Account overview'")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	session, err := agent.NewSessionContext(ctx, agent.SessionConfig{
		Headless: false,
		Timeout:  30 * time.Second,
	})
	if err != nil {
		return err
	}
	defer func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer closeCancel()
		_ = session.CloseContext(closeCtx)
	}()

	if _, err := session.NavigateWithPostconditionContext(ctx, *url, agent.Postcondition{}); err != nil {
		return err
	}

	condition := &agent.Postcondition{Text: *expected}
	result, err := session.HandoffToHumanContext(ctx, "Complete the browser step", func(ctx context.Context, request agent.HumanHandoffRequest) error {
		fmt.Printf("Browser is open on target %s. URL: %s\n", request.TargetID, request.URL)
		fmt.Println("Complete the requested step in the browser window, then press Enter here.")
		inputReady := make(chan error, 1)
		go func() {
			_, err := bufio.NewReader(os.Stdin).ReadString('\n')
			inputReady <- err
		}()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-inputReady:
			return err
		}
	}, condition)
	if err != nil {
		return err
	}
	fmt.Printf("Outcome: %s; verified: %t; evidence: %s\n", result.Outcome, result.Verified, result.Evidence)
	if !result.Verified {
		return fmt.Errorf("expected state was not verified")
	}
	return nil
}
