package rules

import (
	"os"

	"github.com/redis/go-redis/v9"
)

func clients() {
	// ruleid: redis-hardcoded-password
	_ = redis.NewClient(&redis.Options{Addr: "localhost:6379", Password: "hunter2"})
	// ok: redis-hardcoded-password
	_ = redis.NewClient(&redis.Options{Addr: "localhost:6379", Password: os.Getenv("REDIS_PASSWORD")})
	// ok: redis-hardcoded-password
	_ = redis.NewClient(&redis.Options{Addr: "localhost:6379"})
}
