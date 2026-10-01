package config

import "testing"

func TestTheBrowserIsOfferedOnlyWhereASandboxedSessionAdmitsIt(t *testing.T) {
	for _, engine := range []string{"claude", "codex"} {
		if err := (Browser{On: true, Name: "Work"}).Validate(engine); err != nil {
			t.Errorf("%s: %v", engine, err)
		}
	}
	for _, engine := range []string{"grok", "openai-compatible"} {
		if err := (Browser{On: true}).Validate(engine); err == nil {
			t.Errorf("%s was given the browser", engine)
		}
		if err := (Browser{Name: "Work"}).Validate(engine); err != nil {
			t.Errorf("%s refused a browser that is off: %v", engine, err)
		}
	}
}
