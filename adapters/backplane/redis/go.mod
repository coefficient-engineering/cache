module github.com/coefficient-engineering/cache/adapters/backplane/redis

go 1.26.0

// v1.0.0 was published accidentally. v1.0.1 contains only this retraction.
retract (
	v1.0.1
	v1.0.0
	v0.3.1 // Raised the minimum Go version to 1.26 in a patch release; use v0.4.0.
)

require (
	github.com/alicebob/miniredis/v2 v2.40.0
	github.com/coefficient-engineering/cache v0.2.0
	github.com/google/uuid v1.6.0
	github.com/redis/go-redis/v9 v9.23.0
)

require (
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/yuin/gopher-lua v1.1.1 // indirect
	go.uber.org/atomic v1.12.0 // indirect
	golang.org/x/sys v0.48.0 // indirect
)
