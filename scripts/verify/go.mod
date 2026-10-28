// Verification harness: an unpublished consumer module that imports the
// library the way an application does. Resolved locally through go.work.
module github.com/coefficient-engineering/cache/scripts/verify

go 1.26.0

require (
	github.com/coefficient-engineering/cache v0.2.0
	github.com/coefficient-engineering/cache/adapters/backplane/redis v0.2.0
	github.com/coefficient-engineering/cache/adapters/l2/redis v0.2.0
	github.com/redis/go-redis/v9 v9.23.0
)

require (
	github.com/alicebob/miniredis/v2 v2.40.0 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/google/uuid v1.6.0 // indirect
	go.uber.org/atomic v1.12.0 // indirect
	golang.org/x/sync v0.24.0 // indirect
	golang.org/x/sys v0.48.0 // indirect
)
