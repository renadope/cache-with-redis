package limiter

import (
	"context"
	"errors"
	"fmt"
	"github.com/go-redsync/redsync/v4"
	"github.com/go-redsync/redsync/v4/redis/goredis/v9"
	"github.com/redis/go-redis/v9"
	"log"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"
)

var ErrLessThanOrEqualToZero = errors.New("value cannot be less than or equal to zero")

type RateLimiterSlidingWindowLog struct {
	client *redis.Client
	logger *slog.Logger
	rs     *redsync.Redsync
}

type SlidingWindowConfig struct {
	Limit  int
	Burst  int
	Window time.Duration
	Prefix string
}

var DefaultSlidingWindowConfig = SlidingWindowConfig{
	Limit:  100,
	Burst:  0,
	Window: time.Minute,
	Prefix: "rate_limit",
}

type SlidingWindowOption func(config *SlidingWindowConfig)

func WithLimit(limit int) SlidingWindowOption {
	return func(config *SlidingWindowConfig) {
		if limit <= 0 {
			log.Panicf("limit: %v", ErrLessThanOrEqualToZero)
		}
		config.Limit = limit
	}
}
func WithBurst(burst int) SlidingWindowOption {
	return func(config *SlidingWindowConfig) {
		if burst < 0 {
			log.Panicln("burst cannot be less than zero")
		}
		config.Burst = burst
	}
}
func WithWindow(window time.Duration) SlidingWindowOption {
	return func(config *SlidingWindowConfig) {
		if window <= 0 {
			log.Panicf("window: %v", ErrLessThanOrEqualToZero)
		}
		config.Window = window
	}
}
func WithDefaultPrefix(prefix string) SlidingWindowOption {
	return func(config *SlidingWindowConfig) {
		if strings.TrimSpace(prefix) == "" {
			log.Panicln("prefix cannot be empty")
		}
		config.Prefix = prefix
	}
}

func NewRateLimiter(client *redis.Client, logger *slog.Logger) (*RateLimiterSlidingWindowLog, error) {
	if client == nil {
		return nil, fmt.Errorf("redis client cannot be nil")
	}
	pool := goredis.NewPool(client)
	rs := redsync.New(pool)
	if logger == nil {
		logger = slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
			Level: slog.LevelDebug,
		}))
	}
	return &RateLimiterSlidingWindowLog{
		client: client,
		logger: logger,
		rs:     rs,
	}, nil
}

func (rl *RateLimiterSlidingWindowLog) AllowUsingSlidingWindowLogWithWatch(
	ctx context.Context,
	key string,
	opts ...SlidingWindowOption,
) (bool, error) {

	if strings.TrimSpace(key) == "" {
		return false, fmt.Errorf("key should not be empty")
	}
	config := DefaultSlidingWindowConfig
	for _, opt := range opts {
		opt(&config)
	}

	key = fmt.Sprintf("%s:%s", config.Prefix, key)
	nowMS := time.Now().UnixMilli()
	windowStartMS := nowMS - config.Window.Milliseconds()

	rl.logger.Debug("Sliding Window log Config Options",
		slog.String("key", key),
		slog.Int("limit", config.Limit),
		slog.Int("burst", config.Burst),
		slog.Int("window (milli seconds)", int(config.Window.Milliseconds())),
	)

	rl.logger.Debug("Windows",
		slog.Int("nowMS:", int(nowMS)),
		slog.Int("windowStartMS:", int(windowStartMS)),
	)

	var allowed bool

	err := rl.client.Watch(ctx, func(tx *redis.Tx) error {
		pipe := tx.Pipeline()

		removeCmd := pipe.ZRemRangeByScore(ctx, key, "0", strconv.FormatInt(windowStartMS, 10))
		countCmd := pipe.ZCard(ctx, key)
		_, err := pipe.Exec(ctx)
		if errors.Is(err, redis.TxFailedErr) {
			panic(err)
		}
		if err != nil {
			return fmt.Errorf("error executing pipeline: %w", err)
		}
		removed, err := removeCmd.Result()
		if err != nil {
			return fmt.Errorf("error retrieving removal count: %w", err)
		}
		if removed > 0 {
			rl.logger.Debug("Removal Stats",
				slog.String("key", key),
				slog.Int64("count of removed", removed),
				slog.Int64("windowStartMS:", windowStartMS),
			)
		}

		count, err := countCmd.Result()
		if err != nil {
			return fmt.Errorf("error retrieving count: %w", err)
		}
		rl.logger.Debug("Current Count",
			slog.Int64("count", count),
		)
		allowed = count < int64(config.Limit+config.Burst)
		if !allowed {
			rl.logger.Warn("Rate limit reached!",
				slog.String("key", key),
				slog.Int("limit", config.Limit+config.Burst),
			)
			return nil
		}
		pipe.ZAdd(ctx, key, redis.Z{
			Score:  float64(nowMS),
			Member: nowMS,
		})
		pipe.Expire(ctx, key, config.Window+time.Minute)
		_, err = pipe.Exec(ctx)
		if errors.Is(err, redis.TxFailedErr) {
			panic(err)
		}
		return err
	})
	if errors.Is(err, redis.TxFailedErr) {
		panic(err)
	}

	if err != nil {
		return false, fmt.Errorf("transaction failed: %w", err)
	}
	return allowed, nil
}

func (rl *RateLimiterSlidingWindowLog) AllowUsingSlidingWindowLogWithWatch2(

	ctx context.Context,
	key string,
	opts ...SlidingWindowOption,

) (bool, error) {
	config := DefaultSlidingWindowConfig
	for _, opt := range opts {
		opt(&config)
	}

	key = fmt.Sprintf("%s:%s", config.Prefix, key)
	var allowed bool

	maxRetries := 50
	for attempt := 1; attempt <= maxRetries; attempt++ {
		err := rl.client.Watch(ctx, func(tx *redis.Tx) error {
			now := time.Now()
			windowStartMS := now.Add(-config.Window).UnixMilli()
			nowMS := now.UnixMilli()

			cmds, err := tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {

				pipe.ZRemRangeByScore(ctx, key, "0", strconv.FormatInt(windowStartMS, 10))
				pipe.ZCard(ctx, key)
				pipe.ZAdd(ctx, key, redis.Z{
					Score:  float64(nowMS),
					Member: nowMS,
				})
				pipe.Expire(ctx, key, config.Window+time.Minute)
				return nil
			})
			if err != nil {
				return fmt.Errorf("error executing pipeline: %w", err)
			}

			removeCmd := cmds[0].(*redis.IntCmd)
			countCmd := cmds[1].(*redis.IntCmd)
			removed, err := removeCmd.Result()
			if err != nil {
				return fmt.Errorf("error retrieving removal count: %w", err)
			}

			count, err := countCmd.Result()
			if err != nil {
				return fmt.Errorf("error retrieving count: %w", err)
			}

			if removed > 0 {
				rl.logger.Debug("Removal Stats",
					slog.String("key", key),
					slog.Int64("count of removed", removed),
					slog.Int64("windowStartMS:", windowStartMS),
				)
			}

			rl.logger.Debug("Current Count",
				slog.Int64("count", count),
			)

			allowed = count < int64(config.Limit+config.Burst)
			if !allowed {
				rl.logger.Warn("Rate limit reached!",
					slog.String("key", key),
					slog.Int("limit", config.Limit+config.Burst),
				)
				// If not allowed, we need to remove the entry we just added
				tx.ZRem(ctx, key, nowMS)
			}

			return nil
		}, key)
		if err == nil {
			return allowed, nil
		}
		if errors.Is(err, redis.TxFailedErr) {
			rl.logger.Info("Optimistic lock conflict, retrying",
				slog.Int("attempt", attempt+1),
				slog.String("key", key),
			)
			time.Sleep(2 * time.Millisecond)
			continue
		}
		return false, fmt.Errorf("transaction failed: %w", err)

	}
	return false, fmt.Errorf("failed to execute rate limiting after %d attempts", maxRetries)
}

const rateLimiterScript = `
local key = KEYS[1]
local now = tonumber(ARGV[1])
local window = tonumber(ARGV[2])
local limit = tonumber(ARGV[3])

-- Remove old entries
redis.call('ZREMRANGEBYSCORE', key, 0, now - window)

-- Count current entries
local count = redis.call('ZCARD', key)

-- Check if under limit
if count < limit then
    -- Add new entry
    redis.call('ZADD', key, now, now)
    -- Set expiration
    redis.call('EXPIRE', key, window / 1000 + 60)
    return 1
else
    return 0
end
`

func (rl *RateLimiterSlidingWindowLog) AllowUsingSlidingWindowLogWithScript(
	ctx context.Context,
	key string,
	opts ...SlidingWindowOption,
) (bool, error) {
	config := DefaultSlidingWindowConfig
	for _, opt := range opts {
		opt(&config)
	}

	key = fmt.Sprintf("%s:%s", config.Prefix, key)
	now := time.Now().UnixMilli()
	windowMS := config.Window.Milliseconds()

	result, err := rl.client.Eval(
		ctx,
		rateLimiterScript,
		[]string{key},
		now,
		windowMS,
		config.Limit+config.Burst,
	).Result()
	if err != nil {
		return false, fmt.Errorf("failed to execute rate limiter script: %w", err)
	}

	allowed := result.(int64) == 1
	return allowed, nil
}

func (rl *RateLimiterSlidingWindowLog) AllowUsingSlidingWindowLogWithLock(
	ctx context.Context,
	key string,
	opts ...SlidingWindowOption,
) (bool, error) {
	config := DefaultSlidingWindowConfig
	for _, opt := range opts {
		opt(&config)
	}

	key = fmt.Sprintf("%s:%s", config.Prefix, key)
	lockKey := fmt.Sprintf("lock:%s", key)
	mutex := rl.rs.NewMutex(
		lockKey,
		redsync.WithExpiry(10*time.Second),
		redsync.WithTries(10),
	)
	err := mutex.LockContext(ctx)
	if err != nil {
		return false, fmt.Errorf("failed to acquire lock:%w", err)
	}

	defer func() {
		unlocked, unlockedErr := mutex.UnlockContext(ctx)
		if unlockedErr != nil || !unlocked {
			//log.Panicf("Probelms unlocking context %s,unlockedErr:%v ", strconv.FormatBool(unlocked), unlockedErr)
			rl.logger.Error("Problems unlocking context",
				slog.Bool("unlocked", unlocked),
				slog.String("error", unlockedErr.Error()),
			)
		}
	}()

	now := time.Now()
	windowStartMS := now.Add(-config.Window).UnixMilli()
	nowMS := now.UnixMilli()

	pipe := rl.client.Pipeline()
	removeCmd := pipe.ZRemRangeByScore(ctx, key, "0", strconv.FormatInt(windowStartMS, 10))
	countCmd := pipe.ZCard(ctx, key)
	_, err = pipe.Exec(ctx)
	if err != nil {
		return false, fmt.Errorf("error executing pipeline: %w", err)
	}
	removed, err := removeCmd.Result()
	if err != nil {
		return false, fmt.Errorf("error retrieving removal count: %w", err)
	}
	count, err := countCmd.Result()
	if err != nil {
		return false, fmt.Errorf("error retrieving count: %w", err)
	}
	if removed > 0 {
		rl.logger.Debug("Removal Stats",
			slog.String("key", key),
			slog.Int64("count of removed", removed),
			slog.Int64("windowStartMS:", windowStartMS),
		)
	}
	rl.logger.Debug("Current Count",
		slog.Int64("count", count),
	)

	allowed := count < int64(config.Limit+config.Burst)
	if !allowed {
		rl.logger.Warn("Rate limit reached!",
			slog.String("key", key),
			slog.Int("limit", config.Limit+config.Burst),
		)
		return false, nil
	}
	pipe = rl.client.Pipeline()
	pipe.ZAdd(ctx, key, redis.Z{
		Score:  float64(nowMS),
		Member: nowMS,
	})
	pipe.Expire(ctx, key, config.Window+(10*time.Second))
	_, err = pipe.Exec(ctx)
	if err != nil {
		return false, fmt.Errorf("error adding new entry: %w", err)
	}
	return true, nil
}
