// demo is the small application and probe used by the runnable example and
// integration tests. It is not included in the Docktinel artifact image.
package main

import (
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"slices"
	"syscall"
	"time"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: demo serve|check|wait [--ignore-term]")
	}
	switch args[0] {
	case "serve":
		srv := &http.Server{Addr: ":8080", Handler: http.FileServer(http.Dir("/srv")), ReadHeaderTimeout: 5 * time.Second}
		return srv.ListenAndServe()
	case "check":
		if _, err := os.Stat("/srv/fail-health"); err == nil {
			return fmt.Errorf("simulated health failure")
		}
		client := &http.Client{Timeout: time.Second}
		resp, err := client.Get("http://127.0.0.1:8080/")
		if err != nil {
			return fmt.Errorf("HTTP not ready: %w", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("unexpected HTTP response: %s", resp.Status)
		}
		return nil
	case "wait":
		if slices.Contains(args[1:], "--ignore-term") {
			signal.Ignore(syscall.SIGTERM)
		}
		if err := os.WriteFile("/tmp/main-ready", nil, 0600); err != nil {
			return err
		}
		for {
			time.Sleep(24 * time.Hour)
		}
	default:
		return fmt.Errorf("unknown demo command %q", args[0])
	}
}
