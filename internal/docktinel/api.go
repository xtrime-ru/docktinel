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
		if err := json.UnmarshalRead(http.MaxBytesReader(w, r.Body, maxJSONSize), &req, json.RejectUnknownMembers(true)); err != nil {
			code := http.StatusBadRequest
			if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
				code = http.StatusRequestEntityTooLarge
			}
			writeAPI(w, code, apiError{err.Error()})
			return
		}
		if req.Reason == "" {
			writeAPI(w, http.StatusBadRequest, apiError{"restart requires a reason"})
			return
		}
		// Complete the response before shutdown can close the connection.
		w.Header().Set("Content-Length", "2")
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

func writeAPI(w http.ResponseWriter, code int, value any) {
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(apiTimeout))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.MarshalWrite(w, value)
}

func callAPI(dir, method, path string, restart *restartRequest, status *Status, timeout time.Duration) error {
	var body io.Reader
	if restart != nil {
		data, err := json.Marshal(restart)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, "http://docktinel"+path, body)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	socket := filepath.Join(dir, "control.sock")
	transport := &http.Transport{
		DisableKeepAlives: true,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			// Retry connection establishment before sending the request.
			dialer := net.Dialer{Timeout: 200 * time.Millisecond}
			for {
				conn, err := dialer.DialContext(ctx, "unix", socket)
				if err == nil {
					return conn, nil
				}
				if !delay(ctx, retryInterval) {
					return nil, fmt.Errorf("connect to daemon: %w", ctx.Err())
				}
			}
		},
	}
	response, err := transport.RoundTrip(req)
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
	if status != nil {
		return json.Unmarshal(data, status)
	}
	return json.Unmarshal(data, &struct{}{})
}

func daemonStatus(dir string, timeout time.Duration) (Status, error) {
	var state Status
	err := callAPI(dir, http.MethodGet, "/v1/status", nil, &state, timeout)
	return state, err
}
