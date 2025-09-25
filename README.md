# redislayer — Go + Redis cache and sliding-window rate limiter

A practical Go project that provides:

- A JSON-based cache backed by Redis with optional singleflight stampede protection
- A Redis-backed sliding-window-log rate limiter with multiple implementations
- Local dev via Docker Compose and comprehensive tests across Redis versions

Key files:

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

### Run connection Test

```
go run ./cmd
```

Effectively, this connects to our redis instance and ensures that it's alive

### Connect to Redis with helper (recommended)

The helper sets up a client, wires structured logging hooks, and pings
Redis: [Go.NewRedisClient()](internal/redis-helpers/redis-helpers.go)

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

Create a cache with a default expiration: [Go.NewCache()](internal/cache/cache.go)

```go
c := cache.NewCache(rdb, 10*time.Second, logger) // default expiry used when passing a negative duration
```

Basic Set/Get: [Go.RedisCache.Set()](internal/cache/cache.go) / [Go.RedisCache.Get()](internal/cache/cache.go)

```go
type User struct { ID string; Name string }

ctx := context.Background()
_ = c.Set(ctx, "user:42", User{ID: "42", Name: "Alice"}, 1*time.Minute)

var u User
if err := c.Get(ctx, "user:42", &u); err != nil {
log.Fatal(err)
}
```

Compute on miss (populate cache only if key is absent): [Go.RedisCache.GetWithOnMissing()](internal/cache/cache.go)

```go
var profile User
err := c.GetWithOnMissing(ctx, "user:99", &profile, func () (any, error) {
// Fetch from DB or service
return User{ID: "99", Name: "Bob"}, nil
})
```

Compute on miss with singleflight (prevents
stampedes): [Go.RedisCache.GetWithOnMissingWithSingleFlight()](internal/cache/cache.go)

Other cache operations:

- Exists over many keys
- Delete multiple keys
- Flush everything
- Flush by pattern
- Health checks

## Using the Rate Limiter

Construct a limiter: [Go.NewRateLimiter()](internal/limiter/rate-limiter.go)

```go
rl, err := limiter.NewRateLimiter(rdb, logger)
if err != nil {
log.Fatal(err)
}
```

Recommended implementation (fast and robust): Lua script
variant [Go.RateLimiterSlidingWindowLog.AllowUsingSlidingWindowLogWithScript()](internal/limiter/rate-limiter.go)

```go
allowed, err := rl.AllowUsingSlidingWindowLogWithScript(
ctx,
"api:GET:/widgets",     // key suffix
limiter.WithLimit(100), // 100 requests
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

- WATCH +
  pipeline: [Go.RateLimiterSlidingWindowLog.AllowUsingSlidingWindowLogWithWatch()](internal/limiter/rate-limiter.go)
- Alternative WATCH
  flow: [Go.RateLimiterSlidingWindowLog.AllowUsingSlidingWindowLogWithWatch2()](internal/limiter/rate-limiter.go)
- Distributed lock (
  redsync): [Go.RateLimiterSlidingWindowLog.AllowUsingSlidingWindowLogWithLock()](internal/limiter/rate-limiter.go)

Configuration options:

- [Go.WithLimit()](internal/limiter/rate-limiter.go)
- [Go.WithBurst()](internal/limiter/rate-limiter.go)
- [Go.WithWindow()](internal/limiter/rate-limiter.go)
- [Go.WithDefaultPrefix()](internal/limiter/rate-limiter.go)

Validation error surfaced for invalid values:

- [Go.ErrLessThanOrEqualToZero](internal/limiter/rate-limiter.go)

## API Reference (clickable to source)

Cache:

- Constructor: [Go.NewCache()](internal/cache/cache.go)
- Methods:
    - [Go.RedisCache.Set()](internal/cache/cache.go)
    - [Go.RedisCache.Get()](internal/cache/cache.go)
    - [Go.RedisCache.GetWithOnMissing()](internal/cache/cache.go)
    - [Go.RedisCache.GetWithOnMissingWithSingleFlight()](internal/cache/cache.go)
    - [Go.RedisCache.Exists()](internal/cache/cache.go)
    - [Go.RedisCache.FlushAll()](internal/cache/cache.go)
    - [Go.RedisCache.FlushByPattern()](internal/cache/cache.go)
    - [Go.RedisCache.DeleteMultipleKeys()](internal/cache/cache.go)
    - [Go.RedisCache.HealthCheck()](internal/cache/cache.go)
    - [Go.RedisCache.RunPeriodicHealthChecks()](internal/cache/cache.go)

Rate Limiter:

- Constructor: [Go.NewRateLimiter()](internal/limiter/rate-limiter.go)
- Allow methods:
    - [Go.RateLimiterSlidingWindowLog.AllowUsingSlidingWindowLogWithWatch()](internal/limiter/rate-limiter.go)
    - [Go.RateLimiterSlidingWindowLog.AllowUsingSlidingWindowLogWithWatch2()](internal/limiter/rate-limiter.go)
    - [Go.RateLimiterSlidingWindowLog.AllowUsingSlidingWindowLogWithScript()](internal/limiter/rate-limiter.go)
    - [Go.RateLimiterSlidingWindowLog.AllowUsingSlidingWindowLogWithLock()](internal/limiter/rate-limiter.go)
- Options:
    - [Go.WithLimit()](internal/limiter/rate-limiter.go)
    - [Go.WithBurst()](internal/limiter/rate-limiter.go)
    - [Go.WithWindow()](internal/limiter/rate-limiter.go)
    - [Go.WithDefaultPrefix()](internal/limiter/rate-limiter.go)
- Defaults:
    - [Go.DefaultSlidingWindowConfig](internal/limiter/rate-limiter.go)

Redis helper (logging hooks + client):

- [Go.NewRedisClient()](internal/redis-helpers/redis-helpers.go)
- Logging hooks:
    - [Go.LoggingHook.DialHook()](internal/redis-helpers/redis-helpers.go)
    - [Go.LoggingHook.ProcessHook()](internal/redis-helpers/redis-helpers.go)
    - [Go.LoggingHook.ProcessPipelineHook()](internal/redis-helpers/redis-helpers.go)

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


- Rate limiter tests: [go test](cmd/redis_rate_limiter_test.go)
  ```
  go test ./cmd -run TestBasicRateLimit -v
  go test ./cmd -run TestConcurrentRateLimiting -v
  ```

Notes:

- Tests spin Redis containers; first runs may be slower while images are pulled.
- Parallel containers are used; ensure enough CPU/memory.

## Design Notes

- Cache values are stored as JSON via encoding/json; you must pass a pointer as dest for Get/OnMissing APIs.
- Singleflight variant deduplicates concurrent cache-miss recomputations per key.
- Rate limiter uses ZSET timestamps and trims entries older than the window; the Lua script variant is generally
  preferred for performance and atomicity.
- Structured logging uses slog with JSON handler configured in both main and helpers.

## Local Development Tips

- Default Redis address in example is localhost:6379 in [Go.main()](cmd/main.go)
- To adjust default cache expiration, pass a duration to [Go.NewCache()](internal/cache/cache.go) (negative duration
  falls back to internal default)