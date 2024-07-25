package main

import (
	"context"
	"fmt"
	"github.com/redis/go-redis/v9"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
	"log"
	"redislayer/internal/limiter"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

type env struct {
	ctx            context.Context
	redisContainer testcontainers.Container
	redisClient    *redis.Client
	limiter        *limiter.RateLimiterSlidingWindowLog
}

var testImages = []struct {
	name  string
	image string
}{
	{name: "Redis 6", image: "redis:6-alpine"},
	{name: "Redis 7", image: "redis:7-alpine"},
	{name: "Redis latest", image: "redis:latest"},
}

func setupRedisTestEnvFromContainer(t *testing.T, container testcontainers.Container) *env {
	t.Helper()
	ctx := context.Background()
	endPoint, err := container.Endpoint(ctx, "")
	if err != nil {
		t.Fatalf("couldn not retrieve end point:%v", err)
	}
	host, err := container.Host(ctx)
	if err != nil {
		t.Fatalf("couldn not retrieve host:%v", err)
	}
	mappedPort, err := container.MappedPort(ctx, "6379")
	if err != nil {
		t.Fatalf("error getting mapped port:%v", err)
	}
	redisClient := redis.NewClient(&redis.Options{
		Addr:            host + ":" + mappedPort.Port(),
		Password:        "",
		DB:              0,
		MaxRetries:      5,
		MinRetryBackoff: 250 * time.Millisecond,
		MaxRetryBackoff: 1 * time.Second,
		DialTimeout:     5 * time.Second,
		ReadTimeout:     3 * time.Second,
		WriteTimeout:    3 * time.Second,
		PoolTimeout:     4 * time.Second,
		PoolSize:        10,
		MinIdleConns:    5,
	})
	log.Println("🏚️ Addr:", host+":"+mappedPort.Port())
	log.Println("🫵 Endpoint:", endPoint)

	lim, err := limiter.NewRateLimiter(redisClient, nil)
	if err != nil {
		t.Fatalf("error setting up rate limiter:%v", err)
	}
	if lim == nil {
		t.Fatalf("limiter is nil, cannot go on.")
	}
	//clear the redis before running tests
	err = redisClient.FlushAll(ctx).Err()
	if err != nil {
		t.Fatalf("could not fliush redis")
	}

	return &env{
		ctx:            ctx,
		redisContainer: container,
		redisClient:    redisClient,
		limiter:        lim,
	}
}

func teardownRedisTestEnv(t *testing.T, e *env) {
	t.Helper()
	if e.redisContainer != nil {
		err := e.redisContainer.Terminate(e.ctx)
		if err != nil {
			t.Fatalf("error terminating container:%v", err)
		}
	}
}

func runTestWithAllRedisImages(t *testing.T, testFunc func(t *testing.T, e *env)) {
	containerRequests := make([]testcontainers.GenericContainerRequest, len(testImages))
	for i, tc := range testImages {
		validName := strings.ReplaceAll(tc.name, " ", "_")
		validName = regexp.MustCompile(`[^a-zA-Z0-9_.-]`).ReplaceAllString(validName, "")
		containerRequests[i] = testcontainers.GenericContainerRequest{
			ContainerRequest: testcontainers.ContainerRequest{
				Image:        tc.image,
				ExposedPorts: []string{"6379/tcp"},
				WaitingFor:   wait.ForLog("Ready to accept connections"),
				Name:         validName,
			},
		}
	}
	results, err := testcontainers.ParallelContainers(
		context.Background(),
		containerRequests,
		testcontainers.ParallelContainersOptions{
			WorkersCount: 10,
		})

	if err != nil {
		t.Fatalf("issue with container setup:%v", err)
	}
	var wg sync.WaitGroup
	for _, container := range results {
		wg.Add(1)
		go func(c testcontainers.Container) {
			defer wg.Done()
			err = container.Start(context.Background())
			if err != nil {
				t.Errorf("Container failed to start:%v", err)
				return
			}
			if container.IsRunning() {
				t.Log("Container is alive!")
			} else if !container.IsRunning() {
				t.Errorf("Container is not running")
				return
			}
			name, err := container.Name(context.Background())
			if err != nil {
				t.Error("error retrieving name")
				return
			}
			t.Run(name, func(t *testing.T) {
				e := setupRedisTestEnvFromContainer(t, container)
				t.Cleanup(func() {
					teardownRedisTestEnv(t, e)
				})
				testFunc(t, e)
			})

		}(container)
	}
	wg.Wait()
}

func testBasicRateLimit(t *testing.T, e *env) {
	t.Helper()
	//ensure test doesn't hang indefinitely in case of bad math
	ctx, cancel := context.WithTimeout(e.ctx, 1*time.Minute)
	t.Cleanup(func() {
		cancel()
	})
	scenarios := []struct {
		name        string
		limit       int
		window      time.Duration
		delay       time.Duration
		numRequests int
		expected    int
	}{
		{name: "T1", limit: 10, window: 1 * time.Second, delay: 10 * time.Millisecond, numRequests: 15, expected: 10},
		{name: "T2", limit: 20, window: 10 * time.Second, delay: 100 * time.Millisecond, numRequests: 100, expected: 25},
		{name: "T3", limit: 1, window: 2999 * time.Millisecond, delay: 1 * time.Millisecond, numRequests: 100, expected: 50},
		{name: "T4", limit: 1, window: 29 * time.Millisecond, delay: 1 * time.Millisecond, numRequests: 100, expected: 50},
	}
	for _, scenario := range scenarios {
		select {
		case <-ctx.Done():
			t.Fatalf("test timed out")
		default:
			t.Run(scenario.name, func(t *testing.T) {
				testRateLimitScenario(t, e, ctx, scenario)
			})
		}
	}

}

func testRateLimitScenario(t *testing.T, e *env, ctx context.Context, scenario struct {
	name        string
	limit       int
	window      time.Duration
	delay       time.Duration
	numRequests int
	expected    int
}) {
	allowedCount := 0
	for i := 0; i < scenario.numRequests; i++ {
		select {
		case <-ctx.Done():
			t.Fatalf("test timed out")
		default:
			allowed, err := e.limiter.AllowUsingSlidingWindowLogWithScript(
				e.ctx,
				fmt.Sprintf("%s-%d-%d", scenario.name, scenario.limit, int64(scenario.window)),
				limiter.WithLimit(scenario.limit),
				limiter.WithDefaultPrefix("test-env"),
				limiter.WithWindow(scenario.window),
			)
			if allowed {
				t.Log("Allowed")
				allowedCount++
			}
			if err != nil {
				t.Fatalf("Error in rate limiting: %v", err)
			}
			time.Sleep(scenario.delay)
		}
		if allowedCount > scenario.expected {
			diff := allowedCount - scenario.expected
			percentageOver := float64(diff) / float64(scenario.expected) * 100

			if percentageOver >= 100 {
				t.Errorf("something went wrong in the rate limiter or your calculation buddy")
			}
			t.Errorf("the allowedCount:%d is greater than the expected value:%d ", allowedCount, scenario.expected)
		}
		t.Logf("allowedCount:%d, expected value:%d ", allowedCount, scenario.expected)
	}

}
func TestBasicRateLimit(t *testing.T) {
	runTestWithAllRedisImages(t, testBasicRateLimit)
}
