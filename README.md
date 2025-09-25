# redislayer — Go + Redis cache and sliding-window rate limiter

A practical Go project that provides:
- A JSON-based cache backed by Redis with optional singleflight stampede protection
- A Redis-backed sliding-window-log rate limiter with multiple implementations
- Local dev via Docker Compose and comprehensive tests across Redis versions

Key files:
- [go.mod](go.mod)
- [cmd/main.go](cmd/main.go)
- [internal/cache/cache.go](internal/cache/cache.go)
- [internal/limiter/rate-limiter.go](internal/limiter/rate-limiter.go)
- [internal/redis-helpers/redis-helpers.go](internal/redis-helpers/redis-helpers.go)
- [docker-compose.yml](docker-compose.yml)

## Features

Cache:
- Set/Get JSON values with per-key expiration
- Compute-on-miss with optional singleflight to prevent thundering herds
- Bulk existence checks, bulk deletion, pattern flush, and health checks
- Structured logging via slog

Rate Limiter:
- Sliding window log strategy stored in Redis ZSET
- Implementations using WATCH/transactions, a Lua script, and a distributed lock via redsync
- Flexible configuration (limit, burst, window, key prefix) with safe option builders

## Quickstart

### Prerequisites
- Go 1.22+
- Docker and Docker Compose (for local Redis and tests)

### Start Redis locally
- Ensure Docker is running
- Use the provided compose file [docker-compose.yml](docker-compose.yml)
- Command:
  ```
  docker compose up -d
  ```

### Run the example app
The sample entrypoint initializes a logger and connects to Redis in [Go.main()](cmd/main.go:18):
```
go run ./cmd
```

### Connect to Redis with helper (recommended)
The helper sets up a client, wires structured logging hooks, and pings Redis: [Go.NewRedisClient()](internal/redis-helpers/redis-helpers.go:118)

Minimal example:
```go
logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))
rdb, err := redis_helpers.NewRedisClient(&redis.Options{
    Addr: "localhost:6379",
    Password: "",
    DB: 0,
}, logger)
if err != nil {
    log.Fatal(err)
}
defer rdb.Close()
```

## Using the Cache

Create a cache with a default expiration: [Go.NewCache()](internal/cache/cache.go:41)
```go
c := cache.NewCache(rdb, 10*time.Second, logger) // default expiry used when passing a negative duration
```

Basic Set/Get: [Go.RedisCache.Set()](internal/cache/cache.go:64) / [Go.RedisCache.Get()](internal/cache/cache.go:87)
```go
type User struct { ID string; Name string }

ctx := context.Background()
_ = c.Set(ctx, "user:42", User{ID: "42", Name: "Alice"}, 1*time.Minute)

var u User
if err := c.Get(ctx, "user:42", &u); err != nil {
    log.Fatal(err)
}
```

Compute on miss (populate cache only if key is absent): [Go.RedisCache.GetWithOnMissing()](internal/cache/cache.go:106)
```go
var profile User
err := c.GetWithOnMissing(ctx, "user:99", &profile, func() (any, error) {
    // Fetch from DB or service
    return User{ID: "99", Name: "Bob"}, nil
})
```

Compute on miss with singleflight (prevents stampedes): [Go.RedisCache.GetWithOnMissingWithSingleFlight()](internal/cache/cache.go:143)

Other cache operations:
- Exists over many keys: [Go.RedisCache.Exists()](internal/cache/cache.go:246)
- Delete multiple keys: [Go.RedisCache.DeleteMultipleKeys()](internal/cache/cache.go:229)
- Flush everything: [Go.RedisCache.FlushAll()](internal/cache/cache.go:201)
- Flush by pattern: [Go.RedisCache.FlushByPattern()](internal/cache/cache.go:210)
- Health checks: [Go.RedisCache.HealthCheck()](internal/cache/cache.go:274), [Go.RedisCache.RunPeriodicHealthChecks()](internal/cache/cache.go:284)

Common errors surfaced:
- [Go.ErrEmptyKey](internal/cache/cache.go:18)
- [Go.ErrInvalidDestination](internal/cache/cache.go:19)

## Using the Rate Limiter

Construct a limiter: [Go.NewRateLimiter()](internal/limiter/rate-limiter.go:76)
```go
rl, err := limiter.NewRateLimiter(rdb, logger)
if err != nil {
    log.Fatal(err)
}
```

Recommended implementation (fast and robust): Lua script variant [Go.RateLimiterSlidingWindowLog.AllowUsingSlidingWindowLogWithScript()](internal/limiter/rate-limiter.go:304)
```go
allowed, err := rl.AllowUsingSlidingWindowLogWithScript(
    ctx,
    "api:GET:/widgets",              // key suffix
    limiter.WithLimit(100),          // 100 requests
    limiter.WithWindow(60*time.Second),
    limiter.WithDefaultPrefix("svc-a"),
)
if err != nil {
    log.Fatal(err)
}
if !allowed {
    // return 429
}
```

Other available implementations:
- WATCH + pipeline: [Go.RateLimiterSlidingWindowLog.AllowUsingSlidingWindowLogWithWatch()](internal/limiter/rate-limiter.go:94)
- Alternative WATCH flow: [Go.RateLimiterSlidingWindowLog.AllowUsingSlidingWindowLogWithWatch2()](internal/limiter/rate-limiter.go:186)
- Distributed lock (redsync): [Go.RateLimiterSlidingWindowLog.AllowUsingSlidingWindowLogWithLock()](internal/limiter/rate-limiter.go:334)

Configuration options:
- [Go.WithLimit()](internal/limiter/rate-limiter.go:43)
- [Go.WithBurst()](internal/limiter/rate-limiter.go:51)
- [Go.WithWindow()](internal/limiter/rate-limiter.go:59)
- [Go.WithDefaultPrefix()](internal/limiter/rate-limiter.go:67)

Validation error surfaced for invalid values:
- [Go.ErrLessThanOrEqualToZero](internal/limiter/rate-limiter.go:19)

## API Reference (clickable to source)

Cache:
- Constructor: [Go.NewCache()](internal/cache/cache.go:41)
- Methods:
  - [Go.RedisCache.Set()](internal/cache/cache.go:64)
  - [Go.RedisCache.Get()](internal/cache/cache.go:87)
  - [Go.RedisCache.GetWithOnMissing()](internal/cache/cache.go:106)
  - [Go.RedisCache.GetWithOnMissingWithSingleFlight()](internal/cache/cache.go:143)
  - [Go.RedisCache.Exists()](internal/cache/cache.go:246)
  - [Go.RedisCache.FlushAll()](internal/cache/cache.go:201)
  - [Go.RedisCache.FlushByPattern()](internal/cache/cache.go:210)
  - [Go.RedisCache.DeleteMultipleKeys()](internal/cache/cache.go:229)
  - [Go.RedisCache.HealthCheck()](internal/cache/cache.go:274)
  - [Go.RedisCache.RunPeriodicHealthChecks()](internal/cache/cache.go:284)

Rate Limiter:
- Constructor: [Go.NewRateLimiter()](internal/limiter/rate-limiter.go:76)
- Allow methods:
  - [Go.RateLimiterSlidingWindowLog.AllowUsingSlidingWindowLogWithWatch()](internal/limiter/rate-limiter.go:94)
  - [Go.RateLimiterSlidingWindowLog.AllowUsingSlidingWindowLogWithWatch2()](internal/limiter/rate-limiter.go:186)
  - [Go.RateLimiterSlidingWindowLog.AllowUsingSlidingWindowLogWithScript()](internal/limiter/rate-limiter.go:304)
  - [Go.RateLimiterSlidingWindowLog.AllowUsingSlidingWindowLogWithLock()](internal/limiter/rate-limiter.go:334)
- Options:
  - [Go.WithLimit()](internal/limiter/rate-limiter.go:43)
  - [Go.WithBurst()](internal/limiter/rate-limiter.go:51)
  - [Go.WithWindow()](internal/limiter/rate-limiter.go:59)
  - [Go.WithDefaultPrefix()](internal/limiter/rate-limiter.go:67)
- Defaults:
  - [Go.DefaultSlidingWindowConfig](internal/limiter/rate-limiter.go:34)

Redis helper (logging hooks + client):
- [Go.NewRedisClient()](internal/redis-helpers/redis-helpers.go:118)
- Logging hooks:
  - [Go.LoggingHook.DialHook()](internal/redis-helpers/redis-helpers.go:18)
  - [Go.LoggingHook.ProcessHook()](internal/redis-helpers/redis-helpers.go:39)
  - [Go.LoggingHook.ProcessPipelineHook()](internal/redis-helpers/redis-helpers.go:71)

## Running Tests

These tests use Testcontainers to run against multiple Redis versions in parallel. You must have Docker running.

- Cache tests: [go test](cmd/redis_cache_test.go)
  ```
  go test ./cmd -run TestSet -v
  go test ./cmd -run TestGetOnMissing -v
  go test ./cmd -run TestExists -v
  go test ./cmd -run TestDeleteKeys -v
  go test ./cmd -run TestGetOnMissingWithSingleFlight -v
  ```
  Entrypoints:
  - [Go.TestSet()](cmd/redis_cache_test.go:503)
  - [Go.TestGetNonExistent()](cmd/redis_cache_test.go:506)
  - [Go.TestGetOnMissing()](cmd/redis_cache_test.go:509)
  - [Go.TestGetOnMissingWithSingleFlight()](cmd/redis_cache_test.go:512)
  - [Go.TestDeleteKeys()](cmd/redis_cache_test.go:515)
  - [Go.TestExists()](cmd/redis_cache_test.go:518)

- Rate limiter tests: [go test](cmd/redis_rate_limiter_test.go)
  ```
  go test ./cmd -run TestBasicRateLimit -v
  go test ./cmd -run TestConcurrentRateLimiting -v
  ```
  Entrypoints:
  - [Go.TestBasicRateLimit()](cmd/redis_rate_limiter_test.go:359)
  - [Go.TestConcurrentRateLimiting()](cmd/redis_rate_limiter_test.go:363)

Notes:
- Tests spin Redis containers; first runs may be slower while images are pulled.
- Parallel containers are used; ensure enough CPU/memory.

## Design Notes

- Cache values are stored as JSON via encoding/json; you must pass a pointer as dest for Get/OnMissing APIs.
- Singleflight variant deduplicates concurrent cache-miss recomputations per key.
- Rate limiter uses ZSET timestamps and trims entries older than the window; the Lua script variant is generally preferred for performance and atomicity.
- Structured logging uses slog with JSON handler configured in both main and helpers.

## Local Development Tips

- Redis persistence is mounted at ./db-data/redis via [docker-compose.yml](docker-compose.yml)
- Default Redis address in example is localhost:6379 in [Go.main()](cmd/main.go:18)
- To adjust default cache expiration, pass a duration to [Go.NewCache()](internal/cache/cache.go:41) (negative duration falls back to internal default)
