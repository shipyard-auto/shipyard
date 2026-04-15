package service

import "testing"

func TestRandomIDGeneratorNewID(t *testing.T) {
	id, err := (RandomIDGenerator{}).NewID(map[string]struct{}{})
	if err != nil {
		t.Fatal(err)
	}
	if len(id) != 6 {
		t.Fatalf("expected 6-char id, got %q", id)
	}
}

