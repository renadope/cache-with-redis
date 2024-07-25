package main

import (
	"context"
	"fmt"
	"github.com/redis/go-redis/v9"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
	"log"
	"math"
	"redislayer/internal/limiter"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
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
				Name:         validName + "rate_lim",
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
	ctx, cancel := context.WithTimeout(e.ctx, 2*time.Minute)
	t.Cleanup(func() {
		cancel()
	})
	scenarios := []struct {
		name        string
		limit       int
		window      time.Duration
		delay       func(int) time.Duration
		numRequests int
		expected    int
	}{
		{name: "T1", limit: 10, window: 1 * time.Second, delay: func(i int) time.Duration { return 10 * time.Millisecond }, numRequests: 15, expected: 10},
		{name: "T2", limit: 20, window: 10 * time.Second, delay: func(i int) time.Duration { return 100 * time.Millisecond }, numRequests: 100, expected: 25},
		{name: "T3", limit: 1, window: 2999 * time.Millisecond, delay: func(i int) time.Duration { return 1 * time.Millisecond }, numRequests: 100, expected: 1},
		{name: "T3.1", limit: 1, window: 2975 * time.Millisecond, delay: func(i int) time.Duration { return 1 * time.Millisecond }, numRequests: 100, expected: 1},
		{name: "T3.2", limit: 1, window: 1397 * time.Millisecond, delay: func(i int) time.Duration { return 1 * time.Millisecond }, numRequests: 100, expected: 1},
		{name: "T4", limit: 1, window: 30 * time.Millisecond, delay: func(i int) time.Duration { return 1 * time.Millisecond }, numRequests: 100, expected: 10},
		{name: "T5", limit: 100, window: 5 * time.Second, delay: func(i int) time.Duration { return 13 * time.Millisecond }, numRequests: 100, expected: 100},
		{name: "T6", limit: 1000, window: 1000 * time.Second, delay: func(i int) time.Duration { return 1 * time.Second }, numRequests: 10, expected: 10},
		{name: "T7", limit: 300, window: 10 * time.Second, delay: func(i int) time.Duration { return 1 * time.Microsecond }, numRequests: 10000, expected: 300},
		{name: "T8", limit: 300, window: 5 * time.Second, delay: func(i int) time.Duration { return 100 * time.Millisecond }, numRequests: 100, expected: 98},
		{name: "T9", limit: 10, window: 1 * time.Second, delay: func(i int) time.Duration {
			switch {
			case i < 50:
				return 1 * time.Millisecond
			case i < 100:
				return 50 * time.Millisecond
			case i < 200:
				return 5 * time.Millisecond
			case i < 300:
				return 10 * time.Millisecond
			case i < 400:
				return 1 * time.Millisecond
			case i < 600:
				return 100 * time.Millisecond
			case i < 800:
				return 1 * time.Millisecond
			default:
				return 20 * time.Millisecond
			}
		}, numRequests: 1000, expected: 300},
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
	delay       func(int) time.Duration
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
				allowedCount++
			}
			if err != nil {
				t.Fatalf("Error in rate limiting: %v", err)
			}
			time.Sleep(scenario.delay(i))
		}
	}
	allowedPercentage := float64(allowedCount) / float64(scenario.numRequests) * 100
	expectedPercentage := float64(scenario.expected) / float64(scenario.numRequests) * 100
	tolerance := 5.0

	diff := math.Abs(allowedPercentage - expectedPercentage)
	if diff > tolerance {
		t.Errorf("Allowance outside tolerance range. Got: %d (%.2f%%), Expected: %d (%.2f%%)",
			allowedCount, allowedPercentage, scenario.expected, expectedPercentage)
	} else {
		t.Logf("Allowance within tolerance. Got: %d (%.2f%%), Expected: %d (%.2f%%)",
			allowedCount, allowedPercentage, scenario.expected, expectedPercentage)
	}

	t.Logf(
		"testName:%s,allowedCount:%d, approx expected value:%d, numRequests sent:%d ",
		scenario.name,
		allowedCount,
		scenario.expected,
		scenario.numRequests,
	)

}

func testConcurrentRateLimiting(t *testing.T, e *env) {
	t.Helper()

	ctx, cancel := context.WithTimeout(e.ctx, 1*time.Minute)
	t.Cleanup(func() {
		cancel()
	})

	type durationFunc = func(int64) time.Duration
	numRequests := 100
	requestsChan := make(chan int, numRequests)
	resultsChan := make(chan bool, numRequests)
	errorChan := make(chan error, numRequests)
	var delayMS durationFunc = func(i int64) time.Duration {
		return time.Duration(i) * time.Millisecond
	}

	go func() {
		for i := 0; i < numRequests; i++ {
			requestsChan <- i
			time.Sleep(delayMS(10))
		}
		close(requestsChan)
	}()

	var wg sync.WaitGroup
	var requestsCounter atomic.Int64
	numWorkers := 100
	for i := 0; i < numWorkers; i++ {
		select {
		case <-ctx.Done():
			t.Fatalf("test timed out")
		default:
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				for range requestsChan {
					allowed, err := e.limiter.AllowUsingSlidingWindowLogWithScript(
						context.Background(),
						"concurrent-testing",
						limiter.WithLimit(100),
						limiter.WithWindow(10*time.Second),
						limiter.WithDefaultPrefix(fmt.Sprintf("routine:%d", i)),
					)
					requestsCounter.Add(1)
					resultsChan <- allowed
					if err != nil {
						errorChan <- err
					}
				}
			}(i)
		}
	}
	go func() {
		wg.Wait()
		close(resultsChan)
		close(errorChan)
	}()
	wg.Wait()

	t.Logf("total requests served:%d", requestsCounter.Load())
	allowed := 0
	denied := 0
	for result := range resultsChan {
		if result {
			allowed++
		} else {
			denied++
		}
	}
	ec := 0
	for er := range errorChan {
		ec++
		log.Println(er)
	}
	log.Printf(
		"Test completed. Allowed: %d, Denied: %d, Total Requests: %d, TotalErrors:%d",
		allowed,
		denied,
		numRequests,
		ec,
	)
}
func TestBasicRateLimit(t *testing.T) {
	runTestWithAllRedisImages(t, testBasicRateLimit)
}

func TestConcurrentRateLimiting(t *testing.T) {
	runTestWithAllRedisImages(t, testConcurrentRateLimiting)
}
