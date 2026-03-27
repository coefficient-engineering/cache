// Package serializer provides the Serializer interface for the core
// cache package.
package serializer

// Serializer encodes and decodes Go values for storage in the L2 adapter.
//
// cache calls Marshal before writing a value to L2 and Unmarshal after
// reading bytes back from L2. The Serializer is given the inner user value
// only. cache then wraps it in an envelope with expiry metadata before
// calling the adapter.
//
// Implementations must be safe for concurrent use.
type Serializer interface {
	// Marshal encodes v into bytes.
	Marshal(v any) ([]byte, error)

	// Unmarshal decodes data into v. v is always a non-nil pointer.
	Unmarshal(data []byte, v any) error
}
