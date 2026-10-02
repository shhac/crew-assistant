package engine

import "testing"

func TestProjectDirectoryToolsRemainStrictWithNullableLinks(t *testing.T) {
	for _, tool := range Tools(false) {
		if tool.Function.Name != "create_project" && tool.Function.Name != "update_project" {
			continue
		}
		schema := tool.Function.Parameters
		properties := schema["properties"].(map[string]any)
		required := schema["required"].([]string)
		if !tool.Function.Strict || schema["additionalProperties"] != false || len(properties) != len(required) {
			t.Fatalf("non-strict project schema: %+v", schema)
		}
		directories := properties["directories"].(map[string]any)
		types := directories["type"].([]string)
		if len(types) != 2 || types[0] != "array" || types[1] != "null" || directories["maxItems"] != 16 {
			t.Fatalf("directory schema=%+v", directories)
		}
	}
}
