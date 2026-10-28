// Package json provides a [serializer.Serializer] using [encoding/json].
//
// This is a stateless, zero-configuration serializer suitable for most
// use cases. Create one as a zero-value struct:
//
//	s := &json.Serializer{}
package json

import (
	"encoding/json"

	"github.com/coefficient-engineering/cache/serializer"
)

// Serializer implements [serializer.Serializer] using [encoding/json].
// It is stateless and safe for concurrent use.
type Serializer struct{}

// Marshal encodes v with [encoding/json.Marshal].
func (*Serializer) Marshal(v any) ([]byte, error) {
	return json.Marshal(v)
}

// Unmarshal decodes data into v with [encoding/json.Unmarshal].
func (*Serializer) Unmarshal(data []byte, v any) error {
	return json.Unmarshal(data, v)
}

var _ serializer.Serializer = (*Serializer)(nil)
