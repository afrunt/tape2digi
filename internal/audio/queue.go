package audio

import (
	"sync"
	"sync/atomic"
)

// Queue hands captured chunks from the audio thread to a consumer goroutine
// without ever blocking the producer. Chunks that do not fit are counted as
// overruns.
type Queue struct {
	ch       chan []byte
	overruns atomic.Uint64
	once     sync.Once
}

// NewQueue creates a queue that holds up to capacity chunks.
func NewQueue(capacity int) *Queue { return &Queue{ch: make(chan []byte, capacity)} }

// Push copies p and enqueues it; it reports false (and counts an overrun) if full.
func (q *Queue) Push(p []byte) bool {
	c := make([]byte, len(p))
	copy(c, p)
	select {
	case q.ch <- c:
		return true
	default:
		q.overruns.Add(1)
		return false
	}
}

// Chunks is the consumer side; it is closed by Close.
func (q *Queue) Chunks() <-chan []byte { return q.ch }

// Overruns is the number of chunks dropped because the consumer was too slow.
func (q *Queue) Overruns() uint64 { return q.overruns.Load() }

// Close ends the stream of chunks after pending ones are consumed. Push must
// not be called after Close.
func (q *Queue) Close() { q.once.Do(func() { close(q.ch) }) }

// Ring is a fixed-size frame-aligned byte ring used for monitor playback.
// Writes overwrite the oldest data when full (bounding latency) and reads are
// padded with silence when empty. Both are counted.
type Ring struct {
	mu        sync.Mutex
	buf       []byte
	frame     int
	r, n      int
	overruns  uint64
	underruns uint64
}

// NewRing creates a ring of capacity bytes (rounded down to whole frames).
func NewRing(capacity, frameSize int) *Ring {
	capacity -= capacity % frameSize
	return &Ring{buf: make([]byte, capacity), frame: frameSize}
}

// Write stores p, dropping the oldest frames if there is not enough room.
func (r *Ring) Write(p []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p = p[:len(p)-len(p)%r.frame]
	if len(p) > len(r.buf) {
		r.overruns += uint64(len(p) - len(r.buf))
		p = p[len(p)-len(r.buf):]
	}
	if over := r.n + len(p) - len(r.buf); over > 0 {
		r.r = (r.r + over) % len(r.buf)
		r.n -= over
		r.overruns += uint64(over)
	}
	w := (r.r + r.n) % len(r.buf)
	c := copy(r.buf[w:], p)
	copy(r.buf, p[c:])
	r.n += len(p)
}

// Read fills out with buffered data and pads the remainder with zeros. It
// returns the number of real bytes read. len(out) should be frame-aligned.
func (r *Ring) Read(out []byte) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := len(out)
	if n > r.n {
		n = r.n - r.n%r.frame
	}
	c := copy(out[:n], r.buf[r.r:])
	copy(out[c:n], r.buf)
	r.r = (r.r + n) % len(r.buf)
	r.n -= n
	if n < len(out) {
		clear(out[n:])
		r.underruns++
	}
	return n
}

// Stats returns dropped bytes (overruns) and the count of padded reads (underruns).
func (r *Ring) Stats() (overruns, underruns uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.overruns, r.underruns
}

// Reset discards buffered audio.
func (r *Ring) Reset() {
	r.mu.Lock()
	r.r, r.n = 0, 0
	r.mu.Unlock()
}

// Len returns the number of buffered bytes.
func (r *Ring) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.n
}

// Free returns how many more bytes fit without dropping audio.
func (r *Ring) Free() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.buf) - r.n
}
