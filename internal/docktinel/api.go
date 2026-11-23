package docktinel

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"time"
)

func (d *Daemon) controlServer() *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/status", func(w http.ResponseWriter, r *http.Request) {
		writeAPI(w, http.StatusOK, d.snapshot())
	})
	mux.HandleFunc("POST /v1/restart", func(w http.ResponseWriter, r *http.Request) {
		var req restartRequest
		if !decodeAPI(w, r, &req) {
			return
		}
		if req.Reason == "" {
			writeAPI(w, http.StatusBadRequest, apiError{"restart requires a reason"})
			return
		}
		writeAPI(w, http.StatusOK, struct{}{})
		_ = http.NewResponseController(w).Flush()
		d.stop(req.Reason, true)
	})
	return &http.Server{
		Handler: mux, ReadHeaderTimeout: apiTimeout,
		ReadTimeout: apiTimeout, IdleTimeout: 30 * time.Second,
		MaxHeaderBytes: 16 << 10,
	}
}

func decodeAPI(w http.ResponseWriter, r *http.Request, value any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONSize)
	err := json.UnmarshalRead(r.Body, value, json.RejectUnknownMembers(true))
	if err == nil {
		return true
	}
	code := http.StatusBadRequest
	if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
		code = http.StatusRequestEntityTooLarge
	}
	writeAPI(w, code, apiError{err.Error()})
	return false
}

func writeAPI(w http.ResponseWriter, code int, value any) {
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(apiTimeout))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.MarshalWrite(w, value)
}

func callAPI(dir, method, path string, body, result any, timeout time.Duration) error {
	if timeout <= 0 {
		return errors.New("API timeout must be positive")
	}
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		// Retry only connection establishment: never replay a mutation after sending it.
		dialer := net.Dialer{Timeout: 200 * time.Millisecond}
		for {
			conn, err := dialer.DialContext(ctx, "unix", filepath.Join(dir, "control.sock"))
			if err == nil {
				return conn, nil
			}
			if !delay(ctx, retryInterval) {
				return nil, fmt.Errorf("connect to daemon: %w", ctx.Err())
			}
		}
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport}
	req, err := http.NewRequestWithContext(ctx, method, "http://docktinel"+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := client.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	data, err := readJSONBytes(response.Body)
	if err != nil {
		return err
	}
	if response.StatusCode != http.StatusOK {
		var failure apiError
		if err := json.Unmarshal(data, &failure); err == nil && failure.Error != "" {
			return errors.New(failure.Error)
		}
		return fmt.Errorf("daemon API: %s", response.Status)
	}
	if result == nil {
		result = &struct{}{}
	}
	return json.Unmarshal(data, result)
}

func daemonStatus(dir string, timeout time.Duration) (Status, error) {
	var state Status
	err := callAPI(dir, http.MethodGet, "/v1/status", nil, &state, timeout)
	return state, err
}
