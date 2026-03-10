package qrpc

import "sync"

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
