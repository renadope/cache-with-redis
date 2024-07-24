package cache

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/redis/go-redis/v9"
	"log/slog"
	"os"
	"strings"
	"time"
)

var (
	ErrEmptyKey           = errors.New("key should not be blank, key must be non-empty")
	ErrInvalidDestination = errors.New("destination is nil")
)

type onMissingFunc func() (any, error)

type Cache interface {
	Set(ctx context.Context, key string, value any, expiration time.Duration) error
	Get(ctx context.Context, key string, dest any) error
	GetWithOnMissing(ctx context.Context, key string, dest any, onMissingFunc onMissingFunc) error
	Exists(ctx context.Context, keys ...string) (map[string]bool, error)
	FlushAll(ctx context.Context) error
	FlushByPattern(ctx context.Context, pattern string) error
	DeleteMultipleKeys(ctx context.Context, keys ...string) error
}
type RedisCache struct {
	client            *redis.Client
	defaultExpiration time.Duration
	Logger            *slog.Logger
}

func NewCache(client *redis.Client, defaultExpiration time.Duration, logger *slog.Logger) *RedisCache {
	if defaultExpiration < 0 {
		defaultExpiration = 5 * time.Minute
	}
	if logger == nil {
		logger = slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
			Level: slog.LevelDebug,
		}))
	}
	if client == nil {
		logger.Error("redis client is nil, unable to create cache")
		return nil
	}
	c := &RedisCache{
		client:            client,
		defaultExpiration: defaultExpiration,
		Logger:            logger,
	}
	c.RunPeriodicHealthChecks(30 * time.Second)
	return c
}

func (c *RedisCache) Set(ctx context.Context, key string, value any, expiration time.Duration) error {
	if value == nil {
		return fmt.Errorf("cannot store nil value")
	}
	if c.checkForEmptyString(key) {
		return ErrEmptyKey
	}
	if expiration < 0 {
		expiration = c.defaultExpiration
	}

	j, err := json.Marshal(value)
	if err != nil {
		return err
	}
	cmd := c.client.Set(ctx, key, j, expiration)
	err = cmd.Err()
	if err != nil {
		return err
	}
	return nil
}

func (c *RedisCache) Get(ctx context.Context, key string, dest any) error {
	if c.checkForEmptyString(key) {
		return ErrEmptyKey
	}
	if dest == nil {
		return ErrInvalidDestination
	}
	val, err := c.client.Get(ctx, key).Result()
	if err != nil {
		return err
	}
	err = json.Unmarshal([]byte(val), dest)
	if err != nil {
		return fmt.Errorf("error unmarshaling data:%w", err)
	}
	return nil

}

func (c *RedisCache) GetWithOnMissing(ctx context.Context, key string, dest any, onMissingFunc onMissingFunc) error {
	if c.checkForEmptyString(key) {
		return ErrEmptyKey
	}
	if dest == nil {
		return ErrInvalidDestination
	}

	val, err := c.client.Get(ctx, key).Result()
	if errors.Is(err, redis.Nil) {
		c.Logger.Info("cache miss, going to onMissingFunc", slog.String("key", key))
		if onMissingFunc == nil {
			return fmt.Errorf("key not found and no onMissing function provided")
		}
		newVal, onMissingErr := onMissingFunc()
		if onMissingErr != nil {
			return fmt.Errorf("onMissing function failed: %w", onMissingErr)
		}
		err = c.Set(ctx, key, newVal, c.defaultExpiration)
		if err != nil {
			return fmt.Errorf("failed to set new value in cache: %w", err)
		}
		jsonVal, marshalErr := json.Marshal(newVal)
		if marshalErr != nil {
			return fmt.Errorf("failed to marshal new value: %w", marshalErr)
		}
		val = string(jsonVal)
	} else if err != nil {
		return fmt.Errorf("something went wrong getting key (%s):%w", key, err)
	}
	err = json.Unmarshal([]byte(val), dest)
	if err != nil {
		return fmt.Errorf("error unmarshaling data:%w", err)
	}
	return nil
}

func (c *RedisCache) FlushAll(ctx context.Context) error {
	err := c.client.FlushAll(ctx).Err()
	if err != nil {
		return fmt.Errorf("failed to flush cache: %w", err)
	}
	c.Logger.Info("Cache has been flushed 🚽")
	return nil
}

func (c *RedisCache) FlushByPattern(ctx context.Context, pattern string) error {
	if c.checkForEmptyString(pattern) {
		return errors.New("pattern cannot be empty")
	}
	iter := c.client.Scan(ctx, 0, pattern, 0).Iterator()
	for iter.Next(ctx) {
		err := c.client.Del(ctx, iter.Val()).Err()
		if err != nil {
			return fmt.Errorf("failed to delete key %s: %w", iter.Val(), err)
		}
	}
	err := iter.Err()
	if err != nil {
		return fmt.Errorf("error during cache flush by pattern %w", err)
	}
	c.Logger.Info("Cache flushed by pattern", slog.String("pattern", pattern))
	return nil
}

func (c *RedisCache) DeleteMultipleKeys(ctx context.Context, keys ...string) error {
	filteredKeys := c.filterKeys(keys...)
	if len(filteredKeys) == 0 {
		return fmt.Errorf("no keys provided")
	}
	res, err := c.client.Del(ctx, keys...).Result()
	if err != nil {
		return fmt.Errorf("failed to delete keys: %w", err)
	}
	c.Logger.Info(
		"Multiple keys deleted from cache",
		slog.Int("count", int(res)),
		slog.Float64("ratio", float64(res)/float64(len(filteredKeys))),
	)
	return nil
}

func (c *RedisCache) Exists(ctx context.Context, keys ...string) (map[string]bool, error) {
	filteredKeys := c.filterKeys(keys...)
	if len(filteredKeys) == 0 {
		return nil, fmt.Errorf("no keys provided")
	}
	pipe := c.client.Pipeline()
	cmds := make(map[string]*redis.IntCmd, len(filteredKeys))
	for _, key := range filteredKeys {
		/*This line doesn't actually execute the Exists command immediately. Instead, it:

		Adds the Exists command to the pipeline queue.
		Returns a *redis.IntCmd object, which is a promise for the future result of this command.*/
		cmds[key] = pipe.Exists(ctx, key)
	}
	/*
		The commands are only sent to Redis and executed when you call:*/
	_, err := pipe.Exec(ctx)
	if err != nil {
		return nil, fmt.Errorf("error checking existence of keys: %w", err)
	}

	result := make(map[string]bool, len(filteredKeys))
	for key, cmd := range cmds {
		result[key] = cmd.Val() > 0
	}
	return result, nil
}

func (c *RedisCache) HealthCheck(ctx context.Context) error {
	err := c.client.Ping(ctx).Err()
	if err != nil {
		c.Logger.Error("redis health check failed", slog.String("error", err.Error()))
		return err
	}
	c.Logger.Debug("Redis health check passed")
	return nil
}

func (c *RedisCache) RunPeriodicHealthChecks(interval time.Duration) {
	ticker := time.NewTicker(interval)
	go func() {
		for {
			select {
			case <-ticker.C:
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				err := c.HealthCheck(ctx)
				if err != nil {
					c.Logger.Error("should really do something about this buddy")
				}
				cancel()
			}
		}
	}()
}

func (c *RedisCache) filterKeys(keys ...string) []string {

	/*The [:0] syntax in Go is a slice operation that creates a new slice with the same underlying array as the original slice, but with a length of zero.
	This is a powerful and efficient technique often used in Go for in-place filtering or modification of slices.*/
	filteredKeys := keys[:0]
	for _, key := range keys {
		if !c.checkForEmptyString(key) {
			filteredKeys = append(filteredKeys, key)
		}
	}
	return filteredKeys
}

func (c *RedisCache) checkForEmptyString(str string) bool {
	return strings.TrimSpace(str) == ""
}
