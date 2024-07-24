package redis_helpers

import (
	"context"
	"fmt"
	"github.com/redis/go-redis/v9"
	"log/slog"
	"math/rand"
	"net"
	"time"
)

type LoggingHook struct {
	logger *slog.Logger
}

func (h LoggingHook) DialHook(next redis.DialHook) redis.DialHook {

	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		h.logger.Debug("Dialing Redis",
			slog.String("network", network),
			slog.String("address", addr))

		conn, err := next(ctx, network, addr)
		if err != nil {
			h.logger.Error("Error connecting to redis")
			return nil, fmt.Errorf("error connecting:%w", err)
		}
		if conn == nil {
			h.logger.Error("Connecting received is nil")
			return nil, fmt.Errorf("received a nil connection when attempting to connect to redis")
		}
		h.logger.Info("connected to redis")
		return conn, err
	}
}

func (h LoggingHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		start := time.Now()
		err := next(ctx, cmd)
		duration := time.Since(start)
		if err != nil {
			h.logger.Error("Redis command failed",
				slog.String("command", cmd.String()),
				slog.Int64("duration", int64(duration)),
				slog.String("error", err.Error()),
			)
			return err
		}
		lengthOfSlowOperation := time.Millisecond * 100
		if duration > lengthOfSlowOperation {
			h.logger.Warn(fmt.Sprintf("Slow command greater than (%d)ms", int64(lengthOfSlowOperation)),
				slog.Int64("duration", int64(duration)),
				slog.String("command", cmd.String()),
			)
		}
		samplePercentage := 0.5
		if rand.Float64() <= samplePercentage {
			h.logger.Debug("Redis command",
				slog.String("command", cmd.String()),
				slog.Int64("duration", int64(duration)),
			)
		}
		return err

	}
}

func (h LoggingHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(ctx context.Context, cmds []redis.Cmder) error {
		start := time.Now()
		err := next(ctx, cmds)
		duration := time.Since(start)
		for _, cmd := range cmds {
			h.logger.Debug("Stats",
				slog.String("command", cmd.String()),
				slog.Int64("duration", int64(duration)),
			)
		}
		return err
	}
}

func (h LoggingHook) BeforeProcess(ctx context.Context, cmd redis.Cmder) (context.Context, error) {
	h.logger.Debug("Redis command", slog.String("cmd", cmd.String()))
	return ctx, nil
}

func (h LoggingHook) AfterProcess(ctx context.Context, cmd redis.Cmder) error {
	err := cmd.Err()
	if err != nil {
		h.logger.Error("Redis command failed",
			slog.String("cmd", cmd.String()),
			slog.String("error", err.Error()))
	}
	return nil
}
func (h LoggingHook) BeforeProcessPipeline(ctx context.Context, cmds []redis.Cmder) (context.Context, error) {
	for _, cmd := range cmds {
		h.logger.Debug("Redis pipeline command", slog.String("cmd", cmd.String()))
	}
	return ctx, nil
}

func (h LoggingHook) AfterProcessPipeline(ctx context.Context, cmds []redis.Cmder) error {
	for _, cmd := range cmds {
		err := cmd.Err()
		if err != nil {
			h.logger.Error("Redis command failed",
				slog.String("cmd", cmd.String()),
				slog.String("error", err.Error()))
		}
	}
	return nil
}
func NewRedisClient(opts *redis.Options, logger *slog.Logger) (*redis.Client, error) {

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rdb := redis.NewClient(opts)
	rdb.AddHook(LoggingHook{logger: logger})
	res, err := rdb.Ping(ctx).Result()
	if err != nil {
		return nil, fmt.Errorf("error connecting to redis:%w", err)
	}
	logger.Info("connected to redis", slog.String("res", res))
	return rdb, nil
}
