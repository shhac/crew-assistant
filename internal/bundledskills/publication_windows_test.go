package bundledskills

import "testing"

func TestWindowsPublication(t *testing.T) {
	state := t.TempDir()
	for _, role := range []string{"designer", "implementer"} {
		first, digest, err := Prepare(state, role, nil)
		if err != nil || len(first.Provided) != 1 {
			t.Fatal(first, err)
		}
		again, next, err := Prepare(state, role, nil)
		if err != nil || next != digest || again.Provided[0].Dir != first.Provided[0].Dir {
			t.Fatal("publication could not be reused", again, err)
		}
	}
}
