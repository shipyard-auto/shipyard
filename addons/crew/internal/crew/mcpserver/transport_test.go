package mcpserver

import (
	"bytes"
	"io"
	"strings"
	"sync"
	"testing"
)

func TestReader_ReadsFrames(t *testing.T) {
	in := `{"a":1}` + "\n" + `{"b":2}` + "\n"
	r := NewReader(strings.NewReader(in))

	got1, err := r.Next()
	if err != nil {
		t.Fatalf("next 1: %v", err)
	}
	if string(got1) != `{"a":1}` {
		t.Fatalf("first frame: %q", got1)
	}
	got2, err := r.Next()
	if err != nil {
		t.Fatalf("next 2: %v", err)
	}
	if string(got2) != `{"b":2}` {
		t.Fatalf("second frame: %q", got2)
	}
	if _, err := r.Next(); err != io.EOF {
		t.Fatalf("want io.EOF, got %v", err)
	}
}

func TestReader_RejectsOversizeFrame(t *testing.T) {
	// Build a frame larger than maxFrameBytes.
	big := strings.Repeat("x", maxFrameBytes+1024) + "\n"
	r := NewReader(strings.NewReader(big))

	_, err := r.Next()
	if err == nil {
		t.Fatalf("expected error on oversize frame")
	}
}

func TestWriter_MarshalsAndDelimits(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)
	if err := w.Write(map[string]int{"x": 7}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if buf.String() != `{"x":7}`+"\n" {
		t.Fatalf("unexpected output: %q", buf.String())
	}
}

func TestWriter_ConcurrentSafe(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		i := i
		go func() {
			defer wg.Done()
			_ = w.Write(map[string]int{"i": i})
		}()
	}
	wg.Wait()

	// Every line should be a well-formed JSON object on its own.
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 50 {
		t.Fatalf("want 50 lines, got %d", len(lines))
	}
	for _, line := range lines {
		if !strings.HasPrefix(line, `{"i":`) || !strings.HasSuffix(line, `}`) {
			t.Fatalf("corrupted line: %q", line)
		}
	}
}
