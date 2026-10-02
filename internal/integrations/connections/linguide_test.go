package connections

import (
	"os"
	"strings"
	"testing"
)

func TestLinGuidanceFollowsPolicyAndEmbedsOriginal(t *testing.T) {
	for _, writes := range []bool{false, true} {
		guide := LinGuide(writes)
		if !strings.HasPrefix(guide, "Use the lin tool") {
			t.Fatal("missing preamble")
		}
		for _, denied := range []string{"lin auth", "lin file", "lin api", "lin config", "lin mcp", "--file", " archive ", " delete ", " unarchive ", "relation remove", "attachment remove"} {
			if strings.Contains(guide, denied) {
				t.Errorf("guide writes=%v contains %q", writes, denied)
			}
		}
		for _, read := range []string{"issue search", "issue get", "team states"} {
			if !strings.Contains(guide, read) {
				t.Errorf("guide missing %s", read)
			}
		}
		for _, write := range []string{"lin issue new ", "lin issue comment new ", "lin project update "} {
			if strings.Contains(guide, write) != writes {
				t.Errorf("guide writes=%v command %s", writes, write)
			}
		}
		output, err := LinReference("output", writes)
		if err != nil || strings.Contains(output, "## File") || strings.Contains(output, "## Pretty cards") {
			t.Fatal("unavailable output sections retained")
		}
		commands, _ := LinReference("commands", writes)
		for _, denied := range []string{"lin auth", "lin api", "lin file", "lin config", "--file", "relation remove"} {
			if strings.Contains(commands, denied) {
				t.Errorf("reference contains %s", denied)
			}
		}
	}
	for _, path := range []string{"lin_skill/SKILL.md", "lin_skill/references/commands.md", "lin_skill/references/output.md"} {
		embedded, _ := linSkill.ReadFile(path)
		disk, err := os.ReadFile(path)
		if err != nil || string(embedded) != string(disk) {
			t.Fatalf("embed %s: %v", path, err)
		}
	}
	if _, err := LinReference("../../secret", true); err == nil {
		t.Fatal("unknown reference admitted")
	}
}

func TestLinGuidanceDropsRefusedAndEmptySectionsWithoutLosingReads(t *testing.T) {
	for _, writes := range []bool{false, true} {
		for _, source := range []string{"guide", "commands"} {
			text := LinGuide(writes)
			if source == "commands" {
				text, _ = LinReference(source, writes)
			}
			for _, denied := range []string{"## Raw GraphQL", "## Issue Lifecycle", "Persist commonly-used defaults", "These are global flags"} {
				if strings.Contains(text, denied) {
					t.Errorf("%s writes=%v retains %q", source, writes, denied)
				}
			}
			if !writes && strings.Contains(text, "signal, distinct from") {
				t.Errorf("%s retains write comparison", source)
			}
			lines := strings.Split(text, "\n")
			for i, l := range lines {
				if strings.HasPrefix(l, "##") {
					end := i + 1
					for end < len(lines) && !strings.HasPrefix(lines[end], "##") {
						end++
					}
					if strings.TrimSpace(strings.Join(lines[i+1:end], "\n")) == "" {
						t.Errorf("empty section: %s", l)
					}
				}
			}
			for _, read := range []string{"issue comment get", "issue comment replies", "document list"} {
				if !strings.Contains(text, read) {
					t.Errorf("%s lost %s", source, read)
				}
			}
		}
	}
	sample := "## Empty\n\n```bash\nlin issue delete ENG-1\n```\n\n## Mixed\n\n```bash\nlin issue comment new ENG-1 body\nlin issue comment get comment-id\n```"
	filtered := trimLin(sample, false)
	if strings.Contains(filtered, "## Empty") || !strings.Contains(filtered, "comment get") {
		t.Fatal(filtered)
	}
}
