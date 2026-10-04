package checktest

import "sync"

// Tail bounds check output before it reaches the harness, which retains a
// prefix. This leaves room for the terminal coverage report after noisy stages.
type Tail struct {
	mu        sync.Mutex
	Limit     int
	data      []byte
	truncated bool
}

func (b *Tail) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	if len(p) >= b.Limit {
		b.data = append(b.data[:0], p[len(p)-b.Limit:]...)
		b.truncated = true
		return n, nil
	}
	if excess := len(b.data) + len(p) - b.Limit; excess > 0 {
		b.data = append(b.data[:0], b.data[excess:]...)
		b.truncated = true
	}
	b.data = append(b.data, p...)
	return n, nil
}
func (b *Tail) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.truncated {
		return "[earlier check output omitted]\n" + string(b.data)
	}
	return string(b.data)
}
