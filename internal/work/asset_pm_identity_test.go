package work

import (
	"testing"

	"github.com/shhac/crew-assistant/internal/core"
)

func TestLandingAssetCorrectionRequiresUnambiguousIdentity(t *testing.T) {
	yes := true
	task := core.Task{Roles: []core.Role{{Name: "Designer", Kinds: []string{core.RoleDesigner}}}, Criteria: []string{"Frames"}, Unreachable: []core.Unreachable{
		{ID: "first", Source: "landing", Criterion: "Frames", Why: "first generator", Finding: "original first", AssetCreation: &yes},
		{ID: "second", Source: "landing", Criterion: "Frames", Why: "second generator", Finding: "original second", AssetCreation: &yes},
	}}
	for _, reply := range []string{
		`{"blocked_asset":{"requirement":"Frames","why":"code","asset_creation":false}}`,
		`{"blocked_asset":{"report_id":"unknown","requirement":"Frames","why":"code","asset_creation":false}}`,
		`{"blocked_asset":{"report_id":"first","requirement":"Icons","why":"code","asset_creation":false}}`,
	} {
		if u, err := parseLandingAsset(core.Project{}, task, reply); err == nil || u == nil || u.AssetCreation != nil || u.ID == "first" || u.ID == "second" {
			t.Fatal("ambiguous correction accepted", u, err)
		}
	}
	u, err := parseLandingAsset(core.Project{}, task, `{"land":"malformed","how":45,"blocked_asset":{"report_id":"first","requirement":"Frames","why":"code","asset_creation":false}}`)
	if err != nil || u == nil || u.ID != "first" || u.AssetCreation == nil || *u.AssetCreation || u.Finding != "original first" || u.Why != "first generator" {
		t.Fatal(u, err)
	}
}

func TestLandingAssetTextSurvivesMalformedSiblingFields(t *testing.T) {
	task := core.Task{Roles: []core.Role{{Name: "Designer", Kinds: []string{core.RoleDesigner}}}}
	u, err := parseLandingAsset(core.Project{}, task, `{"land":"invalid","how":45,"blocked_asset":{"requirement":"Frames","why":"sandbox","asset_creation":true,"owner_step":"invalid"}}`)
	if err != nil || u == nil || u.AssetCreation == nil || !*u.AssetCreation || u.Finding != "sandbox" {
		t.Fatal("unrelated fields erased the readable obstacle", u, err)
	}
	u, err = parseLandingAsset(core.Project{}, task, `{"land":true,"blocked_asset":{"report_id":45,"requirement":"Frames","why":"sandbox","asset_creation":false}}`)
	if err == nil || u == nil || u.ID == "" || u.AssetCreation != nil || u.Finding != "sandbox" {
		t.Fatal("malformed identity erased or settled the obstacle", u, err)
	}
}
