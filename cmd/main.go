package main

import (
	"github.com/redis/go-redis/v9"
	"log"
	"log/slog"
	"os"
	redis_helpers "redislayer/internal/redis-helpers"
	"time"
)

type application struct {
	RedisConn *redis.Client
	Logger    *slog.Logger
}

func main() {
	log.Println("hello world")
	app := &application{}
	app.Logger = slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))
	redisConn, err := redis_helpers.NewRedisClient(&redis.Options{
		Addr:     "localhost:6379",
		Password: "",
		DB:       0,

		MaxRetries:      5,
		MinRetryBackoff: 250 * time.Millisecond,
		MaxRetryBackoff: 1 * time.Second,

		DialTimeout:  5 * time.Second,
		ReadTimeout:  3 * time.Second,
		WriteTimeout: 3 * time.Second,
		PoolTimeout:  4 * time.Second,

		PoolSize:     100,
		MinIdleConns: 20,
	}, app.Logger)

	if err != nil || redisConn == nil {
		log.Fatalf("failed to connect to redis")
	}
	defer func(redisConn *redis.Client) {
		closeErrRedis := redisConn.Close()
		if closeErrRedis != nil {
			log.Fatalf("error in closing redis connection:%v", closeErrRedis)
		}
	}(redisConn)
	app.RedisConn = redisConn
}
