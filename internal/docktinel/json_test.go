package docktinel

import (
	"bytes"
	"encoding/json/v2"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestAPIDurationRoundTrip(t *testing.T) {
	task := testTask("dns", "check", "true")
	task.Grace = 250 * time.Millisecond
	wire, err := json.Marshal(task, durationMarshalers)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(wire, []byte(`"timeout":1000000000`)) {
		t.Fatal("duration no longer uses numeric nanoseconds:", string(wire))
	}
	var decoded Task
	if err := json.Unmarshal(wire, &decoded, durationUnmarshalers); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(task, decoded) {
		t.Fatalf("round trip changed task: %+v", decoded)
	}
}

func TestJSONRejectsAmbiguousDocuments(t *testing.T) {
	for name, document := range map[string]string{
		"duplicate": `{"phase":"runtime","phase":"startup"}`,
		"trailing":  `{"phase":"runtime"} {"phase":"startup"}`,
		"utf8":      "{\"phase\":\"\xff\"}",
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "status.json")
			if err := os.WriteFile(path, []byte(document), 0600); err != nil {
				t.Fatal(err)
			}
			value, err := readJSON[Status](path)
			if err == nil {
				t.Fatal("accepted invalid state document")
			}
			if !reflect.DeepEqual(value, Status{}) {
				t.Fatal("invalid document leaked a partial state", value)
			}
		})
	}
	w := httptest.NewRecorder()
	handler := (&Daemon{}).controlServer().Handler
	handler.ServeHTTP(w, httptest.NewRequest("POST", "/v1/restart", strings.NewReader(`{"reason":"one","reason":"two"}`)))
	if w.Code != 400 {
		t.Fatal("accepted duplicate API member", w.Code)
	}
}

func TestStatusOmitsZeroFields(t *testing.T) {
	b, err := json.Marshal(Status{Tasks: []TaskStatus{{Name: "pending"}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"stop_deadline", "last_started", "last_completed", "due", "process"} {
		if bytes.Contains(b, []byte(`"`+key+`"`)) {
			t.Fatal("zero field was not omitted:", key, string(b))
		}
	}
}

func TestStateRejectsOversizeValidJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "status.json")
	data := append([]byte(`{"phase":"runtime"}`), bytes.Repeat([]byte(" "), maxJSONSize)...)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readJSON[Status](path); err == nil {
		t.Fatal("accepted oversized document hidden by trailing whitespace")
	}
}
