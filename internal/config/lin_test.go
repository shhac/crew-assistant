package config

import (
	"encoding/json"
	"testing"
)

func TestLinearChangesDefaultOptInAndValidation(t *testing.T) {
	var c Connection
	if err := json.Unmarshal([]byte(`{"id":"linear","name":"Linear","tool":"lin","profiles":["home"]}`), &c); err != nil || c.AllowWrites {
		t.Fatalf("default: %+v %v", c, err)
	}
	c.AllowWrites = true
	data, _ := json.Marshal(c)
	var decoded Connection
	_ = json.Unmarshal(data, &decoded)
	if !decoded.AllowWrites || validateConnections([]Connection{decoded}) != nil {
		t.Fatal("permission did not roundtrip")
	}
	decoded.Tool = "agent-slack"
	if validateConnections([]Connection{decoded}) == nil {
		t.Fatal("other tool accepted writes")
	}
}
