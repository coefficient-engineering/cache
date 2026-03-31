module github.com/coefficient-engineering/cache/adapters/backplane/redis

go 1.25.0

// v1.0.0 was published accidentally. v1.0.1 contains only this retraction.
retract (
	v1.0.1
	v1.0.0
)

require (
	github.com/alicebob/miniredis/v2 v2.37.0
	github.com/coefficient-engineering/cache v0.2.0
	github.com/google/uuid v1.6.0
	github.com/redis/go-redis/v9 v9.18.0
)

require (
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/dgryski/go-rendezvous v0.0.0-20200823014737-9f7001d12a5f // indirect
	github.com/yuin/gopher-lua v1.1.1 // indirect
	go.uber.org/atomic v1.11.0 // indirect
)
