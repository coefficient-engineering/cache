// Package json is an implementation of the cache Serializer using
// encoding/json.
package json

import (
	"encoding/json"

	"github.com/coefficient-engineering/cache/serializer"
)

// Serializer implements serializer.Serializer using encoding/json.
type Serializer struct{}

func (s *Serializer) Marshal(v any) ([]byte, error) {
	return json.Marshal(v)
}

func (s *Serializer) Unmarshal(data []byte, v any) error {
	return json.Unmarshal(data, v)
}

var _ serializer.Serializer = (*Serializer)(nil)
