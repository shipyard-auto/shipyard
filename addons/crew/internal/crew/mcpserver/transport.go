package mcpserver

import (
	"bufio"
	"encoding/json"
	"io"
	"sync"
)

// maxFrameBytes caps the size of a single inbound JSON-RPC frame. Tool
// arguments are usually small, but the cap needs to be comfortably above
// typical structured inputs (e.g. HTML blobs copied into prompts). 1 MiB is
// the same ceiling the Anthropic API response reader uses.
const maxFrameBytes = 1 * 1024 * 1024

// Reader parses newline-delimited JSON frames from the underlying stream.
// It is NOT safe for concurrent use — one goroutine reads, one goroutine
// writes (via Writer), and routing happens in between.
type Reader struct {
	sc *bufio.Scanner
}

// NewReader builds a Reader with the MCP frame size budget. Callers own
// lifecycle of r.
func NewReader(r io.Reader) *Reader {
	sc := bufio.NewScanner(r)
	// Start at 64 KiB, grow up to maxFrameBytes. bufio.Scanner needs both
	// the initial buffer and the max so it can reuse memory across frames.
	sc.Buffer(make([]byte, 64*1024), maxFrameBytes)
	return &Reader{sc: sc}
}

// Next returns the next frame's raw bytes, or io.EOF when the stream is
// cleanly closed. The returned slice is a copy — the scanner's internal
// buffer is reused on the next call.
func (r *Reader) Next() ([]byte, error) {
	if !r.sc.Scan() {
		if err := r.sc.Err(); err != nil {
			return nil, err
		}
		return nil, io.EOF
	}
	b := r.sc.Bytes()
	out := make([]byte, len(b))
	copy(out, b)
	return out, nil
}

// Writer serialises outbound frames and appends a trailing newline. All
// writes go through a mutex so a future concurrent dispatcher cannot
// interleave partial frames on the wire.
type Writer struct {
	mu sync.Mutex
	w  io.Writer
}

// NewWriter returns a Writer around w. Callers own w's lifecycle.
func NewWriter(w io.Writer) *Writer { return &Writer{w: w} }

// Write marshals v as a single JSON line and flushes it atomically.
// Marshal errors surface to the caller; partial writes propagate whatever
// the underlying writer returns.
func (w *Writer) Write(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	w.mu.Lock()
	defer w.mu.Unlock()
	_, err = w.w.Write(data)
	return err
}
