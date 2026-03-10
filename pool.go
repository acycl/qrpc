package qrpc

import (
	"io"
	"sync"
)

// bufPool pools byte slice buffers to reduce per-RPC heap allocations. Pointers
// to slices are stored to avoid boxing the slice header on each Put.
var bufPool = sync.Pool{
	New: func() any {
		b := make([]byte, 0, 4096)
		return &b
	},
}

// poolBuf is a handle to a pooled byte slice buffer. Callers must call release
// when the buffer and all sub-slices derived from it are no longer needed.
type poolBuf struct {
	bp *[]byte
}

// release returns the underlying buffer to the pool. It is safe to call on a
// zero-value poolBuf.
func (b poolBuf) release() {
	if b.bp != nil {
		putBuf(b.bp)
	}
}

// getBuf returns a pooled byte slice pointer with at least the requested
// capacity. The slice length is reset to zero.
func getBuf(capacity int) *[]byte {
	bp := bufPool.Get().(*[]byte)
	if cap(*bp) < capacity {
		*bp = make([]byte, 0, capacity)
	} else {
		*bp = (*bp)[:0]
	}
	return bp
}

// putBuf returns a buffer to the pool. Buffers larger than 64 KiB are dropped
// to prevent the pool from retaining excessively large allocations.
func putBuf(bp *[]byte) {
	if cap(*bp) > 64<<10 {
		return
	}
	*bp = (*bp)[:0]
	bufPool.Put(bp)
}

// maxPrealloc is the maximum number of bytes allocated upfront when reading a
// frame whose size is declared in its header. For sizes at or below this
// threshold the full buffer is allocated in one shot (fast path). For larger
// declared sizes the buffer grows incrementally as data arrives, preventing a
// peer from forcing large heap allocations without sending the corresponding
// bytes.
const maxPrealloc = 64 << 10 // 64 KiB

// readBuf reads exactly n bytes from r into a pooled buffer. For small reads
// (n <= maxPrealloc) the full buffer is allocated upfront. For larger reads the
// buffer grows in chunks as data arrives. The caller must call putBuf on the
// returned pointer when the data is no longer needed.
func readBuf(r io.Reader, n int) (*[]byte, error) {
	initCap := min(n, maxPrealloc)
	bp := getBuf(initCap)

	if n <= maxPrealloc {
		*bp = (*bp)[:n]
		if _, err := io.ReadFull(r, *bp); err != nil {
			putBuf(bp)
			return nil, err
		}
		return bp, nil
	}

	*bp = (*bp)[:0]
	for len(*bp) < n {
		chunk := min(n-len(*bp), maxPrealloc)
		cur := len(*bp)
		if need := cur + chunk; cap(*bp) < need {
			grown := make([]byte, cur, min(2*need, n))
			copy(grown, *bp)
			*bp = grown
		}
		*bp = (*bp)[:cur+chunk]
		if _, err := io.ReadFull(r, (*bp)[cur:cur+chunk]); err != nil {
			putBuf(bp)
			return nil, err
		}
	}
	return bp, nil
}
