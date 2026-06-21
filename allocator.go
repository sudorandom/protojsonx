package protojsonx

import (
	"reflect"
	"unsafe"
)

// BumpAllocator is a pointer-stable monotonic allocator that pre-allocates
// memory chunks to reduce heap allocation overhead and GC pressure when
// deserializing nested submessages.
//
// A BumpAllocator instance is NOT thread-safe and must only be used by one
// goroutine at a time. It can be reset and reused for subsequent operations.
type BumpAllocator struct {
	chunks [][]byte
	curr   int
	off    int
}

// NewBumpAllocator creates a new BumpAllocator with an initial chunk size of 4KB.
func NewBumpAllocator() *BumpAllocator {
	return &BumpAllocator{
		chunks: [][]byte{make([]byte, 4096)},
	}
}

// Reset clears the allocator's internal offset, allowing the pre-allocated memory
// chunks to be reused. Existing pointers allocated from this allocator are invalidated
// and must no longer be accessed.
func (b *BumpAllocator) Reset() {
	b.curr = 0
	b.off = 0
}

// New allocates a new zero value of the specified type from the allocator's memory pool
// and returns a reflect.Value pointing to it.
func (b *BumpAllocator) New(t reflect.Type) reflect.Value {
	size := int(t.Size())
	align := int(t.Align())

	// Align offset to type requirements
	b.off = (b.off + align - 1) &^ (align - 1)

	if b.off+size > len(b.chunks[b.curr]) {
		nextChunkSize := max(size, 4096)
		b.curr++
		if b.curr >= len(b.chunks) {
			b.chunks = append(b.chunks, make([]byte, nextChunkSize))
		} else if len(b.chunks[b.curr]) < nextChunkSize {
			b.chunks[b.curr] = make([]byte, nextChunkSize)
		}
		b.off = 0
	}

	ptr := unsafe.Pointer(&b.chunks[b.curr][b.off])
	b.off += size
	return reflect.NewAt(t, ptr)
}
