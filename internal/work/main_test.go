package work

import (
	"os"
	"testing"
)

// TestMain keeps the operator's own git configuration out of every test, so
// no scratch commit is ever signed with a real key.
func TestMain(m *testing.M) {
	os.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	os.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	var err error
	fixtureRoot, err = os.MkdirTemp("", "work-fixtures-")
	if err != nil {
		panic(err)
	}
	code := m.Run()
	os.RemoveAll(fixtureRoot)
	os.Exit(code)
}

var fixtureRoot string
