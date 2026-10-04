package autopilot

import "testing"

func TestCatalogModes(t *testing.T) {
	catalog := Catalog()
	if len(catalog) != 11 {
		t.Fatal(len(catalog))
	}
	seen := map[string]bool{}
	for _, f := range catalog {
		if seen[f.ID] || f.Available {
			t.Fatalf("duplicate or available: %+v", f)
		}
		seen[f.ID] = true
		want := Suggest
		if f.ID == Operator {
			want = Off
		}
		for _, mode := range []Mode{"", Off, Suggest, Act} {
			s := Settings{Modes: map[string]Mode{f.ID: mode}}
			got, err := s.EffectiveMode(f.ID)
			expected := mode
			if mode == "" {
				expected = want
			}
			if err != nil || got != expected {
				t.Fatalf("%s: %s %v", f.ID, got, err)
			}
		}
	}
	catalog[0].Available = true
	if Catalog()[0].Available {
		t.Fatal("mutable catalog")
	}
	if mode, err := (Settings{}).EffectiveMode("unknown"); err == nil || mode != Off {
		t.Fatal("unknown accepted")
	}
	if (Settings{Modes: map[string]Mode{Operator: "automatic"}}).Validate() == nil {
		t.Fatal("invalid mode accepted")
	}
}
