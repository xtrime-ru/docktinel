package docktinel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"time"
)

const maxAPIBody = 1 << 20

func (d *Daemon) controlServer() *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/status", func(w http.ResponseWriter, r *http.Request) {
		writeAPI(w, http.StatusOK, d.snapshot())
	})
	mux.HandleFunc("POST /v1/tasks", func(w http.ResponseWriter, r *http.Request) {
		var task Task
		if !decodeAPI(w, r, &task) {
			return
		}
		if err := task.Validate(); err != nil {
			writeAPI(w, http.StatusBadRequest, apiError{err.Error()})
			return
		}
		finishAPI(w, d.add(&task))
	})
	mux.HandleFunc("POST /v1/startup", func(w http.ResponseWriter, r *http.Request) {
		if !decodeAPI(w, r, &struct{}{}) {
			return
		}
		// Client cancellation does not cancel container startup.
		finishAPI(w, d.startup())
	})
	mux.HandleFunc("POST /v1/main", func(w http.ResponseWriter, r *http.Request) {
		var main Identity
		if decodeAPI(w, r, &main) {
			finishAPI(w, d.attach(&main))
		}
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
		d.stop(req.Reason, true)
		finishAPI(w, nil)
	})
	return &http.Server{
		Handler: mux, ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second,
		MaxHeaderBytes: 16 << 10,
		// No global WriteTimeout: startup can take minutes. writeAPI applies
		// a deadline only once the operation has finished.
	}
}

func decodeAPI(w http.ResponseWriter, r *http.Request, value any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxAPIBody)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	err := decoder.Decode(value)
	if err == nil {
		var extra any
		if err = decoder.Decode(&extra); err == io.EOF {
			return true
		}
		if err == nil {
			err = errors.New("expected one JSON value")
		}
	}
	code := http.StatusBadRequest
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		code = http.StatusRequestEntityTooLarge
	}
	writeAPI(w, code, apiError{err.Error()})
	return false
}

func finishAPI(w http.ResponseWriter, err error) {
	if err != nil {
		writeAPI(w, http.StatusConflict, apiError{err.Error()})
		return
	}
	writeAPI(w, http.StatusOK, struct{}{})
}

func writeAPI(w http.ResponseWriter, code int, value any) {
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(5 * time.Second))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(value)
}

func callAPI(dir, method, path string, body, result any, timeout time.Duration) error {
	if timeout <= 0 {
		return errors.New("API timeout must be positive")
	}
	if body == nil {
		body = struct{}{}
	}
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		// Retry only connection establishment: never replay a mutation after sending it.
		// This also handles the first add racing daemon startup in the entrypoint.
		dialer := net.Dialer{Timeout: 200 * time.Millisecond}
		for {
			conn, err := dialer.DialContext(ctx, "unix", filepath.Join(dir, "control.sock"))
			if err == nil {
				return conn, nil
			}
			timer := time.NewTimer(25 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, fmt.Errorf("connect to daemon: %w", ctx.Err())
			case <-timer.C:
			}
		}
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport}
	var reader io.Reader
	if method != http.MethodGet {
		reader = bytes.NewReader(data)
	}
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
	decoder := json.NewDecoder(io.LimitReader(response.Body, maxAPIBody))
	if response.StatusCode != http.StatusOK {
		var failure apiError
		if err := decoder.Decode(&failure); err == nil && failure.Error != "" {
			return errors.New(failure.Error)
		}
		return fmt.Errorf("daemon API: %s", response.Status)
	}
	if result == nil {
		result = &struct{}{}
	}
	return decoder.Decode(result)
}
