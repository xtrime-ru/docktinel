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
)

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
