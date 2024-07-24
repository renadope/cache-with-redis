package main

import (
	"github.com/redis/go-redis/v9"
	"log"
	"log/slog"
	"os"
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
}
