package integration

import (
	"context"
	"encoding/json/v2"
	"strings"
	"testing"
)

func TestValidateAndConfirmedRecovery(t *testing.T) {
	f := newFixture(t, `
		printf '%s' '{"tasks":[{"kind":"check","name":"dns","phase":"both","interval":"100ms","successes":2,"failure":"degrade","command":["test","-f","/tmp/available"]}]}' > "$DOCKTINEL_CONFIG"
		docktinel config validate
		test ! -e /run/docktinel/control.sock
		touch /tmp/available
		exec docktinel startup --timeout 10s -- demo wait
	`)
	f.runtime()
	type confirmationStatus struct {
		State string `json:"state"`
		Tasks []struct {
			State     string `json:"state"`
			Successes int    `json:"successes"`
			Required  int    `json:"required_successes"`
		} `json:"tasks"`
	}
	observe := func(ctx context.Context) (confirmationStatus, error) {
		var s confirmationStatus
		output, _, err := f.exec(ctx, "docktinel", "status", "--json")
		if err == nil {
			err = json.Unmarshal([]byte(output), &s)
		}
		return s, err
	}
	state, err := observe(t.Context())
	if err != nil || len(state.Tasks) != 1 || state.Tasks[0].Required != 2 || state.Tasks[0].Successes != 2 {
		t.Fatalf("startup did not confirm readiness: %+v, %v", state, err)
	}
	f.mustExec("rm", "/tmp/available")
	eventually(t, f.status, func(s sentinelStatus) bool { return s.State == "degraded" })
	f.mustExec("touch", "/tmp/available")
	eventually(t, f.status, func(s sentinelStatus) bool { return s.State == "healthy" })
	if output := f.mustExec("docktinel", "status"); !strings.Contains(output, "2/2 successes") {
		t.Fatalf("status lacks confirmation progress: %s", output)
	}
}
