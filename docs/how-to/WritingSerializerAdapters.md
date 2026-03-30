# Writing Serializer Adapters

The `serializer.Serializer` interface encodes and decodes Go values for storage in the L2 distributed cache. The cache calls `Marshal` before writing a value to L2 and `Unmarshal` after reading bytes back.

## The interface

```go
package serializer

type Serializer interface {
    Marshal(v any) ([]byte, error)
    Unmarshal(data []byte, v any) error
}
```

## Method contracts

### `Marshal(v any) ([]byte, error)`

Encode `v` into bytes. `v` is the user's value (the inner value, not the L2 envelope). The cache wraps it in an envelope before passing the envelope to the L2 adapter.

Return an error if the value cannot be encoded.

### `Unmarshal(data []byte, v any) error`

Decode `data` into `v`. `v` is always a non-nil pointer. The implementation should populate the value that `v` points to.

Return an error if the data cannot be decoded.

## Concurrency

All methods must be safe for concurrent use. Multiple goroutines may call `Marshal` and `Unmarshal` simultaneously.

A stateless serializer (like the built-in JSON adapter) satisfies this automatically. A serializer with pooled buffers or shared state must use appropriate synchronization.

## Example: wrapping encoding/json

This is the approach used by the built-in JSON serializer adapter:

```go
package json

import (
    "encoding/json"

    "github.com/coefficient-engineering/cache/serializer"
)

type Serializer struct{}

func (s *Serializer) Marshal(v any) ([]byte, error) {
    return json.Marshal(v)
}

func (s *Serializer) Unmarshal(data []byte, v any) error {
    return json.Unmarshal(data, v)
}

var _ serializer.Serializer = (*Serializer)(nil)
```

## Example: wrapping a binary codec

```go
package msgpack

import (
    "github.com/coefficient-engineering/cache/serializer"
    "github.com/vmihailenco/msgpack/v5"
)

type Serializer struct{}

func (s *Serializer) Marshal(v any) ([]byte, error) {
    return msgpack.Marshal(v)
}

func (s *Serializer) Unmarshal(data []byte, v any) error {
    return msgpack.Unmarshal(data, v)
}

var _ serializer.Serializer = (*Serializer)(nil)
```

## Auto-clone

When `EnableAutoClone` is set in entry options, the cache uses the serializer to deep-clone values returned from L1 (marshal then unmarshal). This means the serializer affects L1 read performance when auto-clone is active. Choose a fast codec if auto-clone is a common configuration in your deployment.

## What the serializer does not handle

The serializer encodes only the inner user value. The cache handles envelope encoding (logical expiry, physical expiry, tags) internally using `encoding/json`. You do not need to account for cache metadata in your serializer implementation.

## Reference implementation

- `adapters/serializer/json/json.go` for the built-in JSON implementation

## See also

- [Using Auto-Clone](UsingAutoClone.md) for the deep-clone feature that depends on the serializer
- [The Adapter Pattern](../explanation/TheAdapterPattern.md) for the design rationale
