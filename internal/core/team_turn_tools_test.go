package core

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestTeamTurnOpeningToolReportJSON(t *testing.T) {
	old := []byte(`{"at":"2026-10-04T12:00:00Z","resumed":true}`)
	var opening TeamTurnOpening
	if err := json.Unmarshal(old, &opening); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(opening)
	if err != nil || strings.Contains(string(raw), "unavailable_tools") {
		t.Fatalf("%s %v", raw, err)
	}
	opening.UnavailableTools = []TeamTurnTool{{Name: "read_file", Reason: "workbench file tools are off"}}
	raw, err = json.Marshal(opening)
	var decoded TeamTurnOpening
	if err != nil || json.Unmarshal(raw, &decoded) != nil || !reflect.DeepEqual(decoded, opening) {
		t.Fatalf("%s %v", raw, err)
	}
}
