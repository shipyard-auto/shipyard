package tools

import (
	"reflect"
	"testing"
)

func TestBuildJSONSchema_Empty(t *testing.T) {
	got := BuildJSONSchema(nil)
	want := map[string]any{"type": "object", "properties": map[string]any{}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("nil: got %v want %v", got, want)
	}
	got = BuildJSONSchema(map[string]string{})
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("empty: got %v want %v", got, want)
	}
}

func TestBuildJSONSchema_KnownTypes(t *testing.T) {
	in := map[string]string{
		"s": "string",
		"n": "number",
		"b": "boolean",
		"o": "object",
		"a": "array",
	}
	got := BuildJSONSchema(in)
	props, ok := got["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties: wrong type: %T", got["properties"])
	}
	for field, wantType := range map[string]string{
		"s": "string", "n": "number", "b": "boolean", "o": "object", "a": "array",
	} {
		entry, ok := props[field].(map[string]any)
		if !ok {
			t.Fatalf("missing field %q in schema: %v", field, props)
		}
		if entry["type"] != wantType {
			t.Errorf("field %q: got type %v want %s", field, entry["type"], wantType)
		}
	}
}

func TestBuildJSONSchema_UnknownTypeFallsBackToString(t *testing.T) {
	got := BuildJSONSchema(map[string]string{"x": "datetime"})
	props := got["properties"].(map[string]any)
	entry := props["x"].(map[string]any)
	if entry["type"] != "string" {
		t.Fatalf("unknown type fallback: got %v want string", entry["type"])
	}
}
