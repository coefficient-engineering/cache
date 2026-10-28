// Verification harness: an unpublished consumer module that imports the
// library the way an application does. Resolved locally through go.work.
module github.com/coefficient-engineering/cache/scripts/verify

go 1.25.0

require (
	github.com/coefficient-engineering/cache v0.2.0
	github.com/coefficient-engineering/cache/adapters/backplane/redis v0.2.0
	github.com/coefficient-engineering/cache/adapters/l2/redis v0.2.0
	github.com/redis/go-redis/v9 v9.18.0
)

require (
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/dgryski/go-rendezvous v0.0.0-20200823014737-9f7001d12a5f // indirect
	github.com/google/uuid v1.6.0 // indirect
	go.uber.org/atomic v1.11.0 // indirect
	golang.org/x/sync v0.20.0 // indirect
)
