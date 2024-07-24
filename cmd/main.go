package main

import (
	"github.com/redis/go-redis/v9"
	"log"
)

type application struct {
	redisConn *redis.Client
}

func main() {
	log.Println("hello world")
}
