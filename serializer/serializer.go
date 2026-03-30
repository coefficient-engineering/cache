// Package serializer defines the [Serializer] interface for encoding and
// decoding Go values for storage in the L2 cache layer.
//
// The cache uses the serializer in two contexts:
//
//  1. L2 reads and writes. Before writing to L2, the cache calls
//     [Serializer.Marshal] on the user value, wraps the resulting bytes in
//     a JSON-encoded envelope, and passes the envelope bytes to the L2
//     adapter. On read, the cache decodes the envelope with encoding/json,
//     then calls [Serializer.Unmarshal] on the inner bytes.
//
//  2. Auto-clone. When EnableAutoClone is set in the entry options, the
//     cache deep-clones L1 values by calling [Serializer.Marshal] then
//     [Serializer.Unmarshal]. This prevents callers from mutating cached
//     pointers.
//
// # Concurrency
//
// All [Serializer] implementations must be safe for concurrent use from
// multiple goroutines.
package serializer

// Serializer encodes and decodes Go values for storage in the L2 adapter.
//
// The cache calls [Serializer.Marshal] before writing a value to L2 and
// [Serializer.Unmarshal] after reading bytes back from L2. The Serializer
// is given the inner user value only — the cache then wraps it in an
// envelope with expiry metadata before calling the L2 adapter.
//
// A Serializer is required whenever an L2 adapter is configured. It is
// also required when EnableAutoClone is set, even without L2.
//
// Implementations must be safe for concurrent use.
type Serializer interface {
	// Marshal encodes v into bytes.
	Marshal(v any) ([]byte, error)

	// Unmarshal decodes data into v. v is always a non-nil pointer.
	Unmarshal(data []byte, v any) error
}
