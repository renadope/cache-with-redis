package main

import (
	"context"
	"fmt"
	"log"
	"redislayer/internal/cache"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

type testEnv struct {
	ctx            context.Context
	redisContainer testcontainers.Container
	redisClient    *redis.Client
	cash           *cache.RedisCache
}

var RedisTestImages = []struct {
	name  string
	image string
}{
	{name: "Redis 6", image: "redis:6-alpine"},
	{name: "Redis 7", image: "redis:7-alpine"},
}

func setupTestEnvFromContainer(t *testing.T, container testcontainers.Container) *testEnv {
	t.Helper()
	ctx := context.Background()

	endPoint, err := container.Endpoint(ctx, "")
	if err != nil {
		t.Fatalf("could not retrieve endpoint:%s", err)
	}

	mappedPort, err := container.MappedPort(ctx, "6379")
	if err != nil {
		t.Fatalf("failed to get container external port: %s", err)
	}
	host, err := container.Host(ctx)
	if err != nil {
		t.Fatalf("failed to get container hort: %s", err)
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

	cash := cache.NewCache(redisClient, 10*time.Second, nil)

	if cash == nil {
		t.Fatal("failed to create cache")
	}

	err = cash.FlushAll(ctx)
	if err != nil {
		t.Fatalf("failed to empty cache before starting: %s", err)
	}

	return &testEnv{
		ctx:            ctx,
		redisContainer: container,
		redisClient:    redisClient,
		cash:           cash,
	}
}

func teardownTestEnv(t *testing.T, env *testEnv) {
	t.Helper()
	err := env.redisClient.Close()
	if err != nil {
		t.Fatalf("error closing redis client")
	}
	if env.redisContainer != nil {
		err := env.redisContainer.Terminate(env.ctx)
		if err != nil {
			t.Fatalf("failed to terminate container: %s", err)
		}
	}
}

func testSet(t *testing.T, env *testEnv) {
	t.Helper()
	err := env.cash.Set(env.ctx, "testkey", "testvalue", time.Minute)
	if err != nil {
		t.Errorf("Set failed: %s", err)
	}

	err = env.cash.Set(env.ctx, "", "testvalue", time.Minute)
	if err == nil {
		t.Errorf("Set (using an empty string) should have failed : %s", err)
	}
}
func testGetNonExistent(t *testing.T, env *testEnv) {
	t.Helper()
	var result string
	err := env.cash.Get(env.ctx, "nonexistentkey", &result)
	if err == nil {
		t.Error("Expected an error when getting non-existent key, got nil")
	}
	err = env.cash.Get(env.ctx, "", &result)
	if err == nil {
		t.Errorf("should return an error when passing an empty string")
	}

	err = env.cash.Get(env.ctx, "somebody", nil)
	if err == nil {
		t.Errorf("should return an error when passing an empty string")
	}

	//don't need to test this really, but paranoia wins this round
	err = env.cash.Get(env.ctx, "", nil)
	if err == nil {
		t.Errorf("should return an error when passing an empty string or nil value")
	}

}
func testGetOnMissing(t *testing.T, env *testEnv) {
	t.Helper()
	t.Run("KeyNotExist_OnMissingSucceeds", func(t *testing.T) {
		key := "missing_key"
		var result string
		onMissing := func() (any, error) {
			return "generated_value", nil
		}
		err := env.cash.GetWithOnMissing(env.ctx, key, &result, onMissing)
		if err != nil {
			t.Errorf("Get with on missing failed")
		}
		if result != "generated_value" {
			t.Errorf("Expected 'generated_value', got '%s'", result)
		}
		var r2 string
		err = env.cash.Get(env.ctx, key, &r2)
		if err != nil {
			t.Errorf("error retrieving fromc cash")
		}
		if r2 != "generated_value" {
			t.Errorf("Expected 'generated_value', got '%s'", result)
		}

	})

	t.Run("KeyNotExist, On missing fails", func(t *testing.T) {
		key := "missing_key2"
		var result string
		onMissing := func() (any, error) {
			return nil, fmt.Errorf("failed to generate value")
		}
		err := env.cash.GetWithOnMissing(env.ctx, key, &result, onMissing)
		if err == nil {
			t.Errorf("Error expected, onMissing returned nil error")
		}

		var r2 string
		err = env.cash.Get(env.ctx, key, &r2)
		if err == nil {
			t.Errorf("should receive an error here, key should not be in cache")
		}

	})
	t.Run("KeyExists", func(t *testing.T) {
		key := "existing key"
		var result string
		var initial = "619"
		err := env.cash.Set(env.ctx, key, initial, 1*time.Minute)
		if err != nil {
			t.Errorf("error setting cache with value:%s", initial)
		}

		onMissing := func() (any, error) {
			return "booyaka should not be used", nil
		}

		err = env.cash.GetWithOnMissing(env.ctx, key, &result, onMissing)
		if err != nil {
			t.Errorf("error retrieving fromc cash")
		}
		if result != initial {
			t.Errorf("retrieved wrong value from cacha")
		}
	})

	t.Run("OnMissingDifferentType", func(t *testing.T) {
		key := "type_mismatch_key"
		var res string
		onMissingFunc := func() (any, error) {
			return 42, nil
		}
		err := env.cash.GetWithOnMissing(env.ctx, key, res, onMissingFunc)
		if err == nil {
			t.Errorf("expected error due to type mismatch, got nil")
		}
	})
}
func testGetOnMissingWithSingleFlight(t *testing.T, env *testEnv) {
	t.Helper()
	t.Run("KeyNotExist_OnMissingSucceeds", func(t *testing.T) {
		key := "missing_key"
		var result string
		onMissing := func() (any, error) {
			return "generated_value", nil
		}
		err := env.cash.GetWithOnMissingWithSingleFlight(env.ctx, key, &result, onMissing)
		if err != nil {
			t.Errorf("Get with on missing failed")
		}
		if result != "generated_value" {
			t.Errorf("Expected 'generated_value', got '%s'", result)
		}
		var r2 string
		err = env.cash.Get(env.ctx, key, &r2)
		if err != nil {
			t.Errorf("error retrieving fromc cash")
		}
		if r2 != "generated_value" {
			t.Errorf("Expected 'generated_value', got '%s'", result)
		}

	})

	t.Run("KeyNotExist, On missing fails", func(t *testing.T) {
		key := "missing_key2"
		var result string
		onMissing := func() (any, error) {
			return nil, fmt.Errorf("failed to generate value")
		}
		err := env.cash.GetWithOnMissingWithSingleFlight(env.ctx, key, &result, onMissing)
		if err == nil {
			t.Errorf("Error expected, onMissing returned nil error")
		}

		var r2 string
		err = env.cash.Get(env.ctx, key, &r2)
		if err == nil {
			t.Errorf("should receive an error here, key should not be in cache")
		}

	})
	t.Run("KeyExists", func(t *testing.T) {
		key := "existing key"
		var result string
		var initial = "619"
		err := env.cash.Set(env.ctx, key, initial, 1*time.Minute)
		if err != nil {
			t.Errorf("error setting cache with value:%s", initial)
		}

		onMissing := func() (any, error) {
			return "booyaka should not be used", nil
		}

		err = env.cash.GetWithOnMissingWithSingleFlight(env.ctx, key, &result, onMissing)
		if err != nil {
			t.Errorf("error retrieving fromc cash")
		}
		if result != initial {
			t.Errorf("retrieved wrong value from cacha")
		}
	})

	t.Run("OnMissingDifferentType", func(t *testing.T) {
		key := "type_mismatch_key"
		var res string
		onMissingFunc := func() (any, error) {
			return 42, nil
		}
		err := env.cash.GetWithOnMissingWithSingleFlight(env.ctx, key, res, onMissingFunc)
		if err == nil {
			t.Errorf("expected error due to type mismatch, got nil")
		}
	})
}
func testExists(t *testing.T, env *testEnv) {
	t.Helper()
	t.Run("Key existence tests", func(t *testing.T) {
		keys := []struct {
			Key   string
			Value any
		}{
			{Key: "k1", Value: "v1"},
			{Key: "k2", Value: 42},
			{Key: "k3", Value: true},
			{Key: "k4", Value: []int{1, 2, 3}},
		}
		keyLimePie := make([]string, 0, len(keys))
		for _, v := range keys {
			err := env.cash.Set(env.ctx, v.Key, v.Value, 4*time.Minute)
			if err != nil {
				t.Fatalf("Error setting values, this should not happen")
			}
			keyLimePie = append(keyLimePie, v.Key)
		}

		res, err := env.cash.Exists(env.ctx, keyLimePie...)
		if err != nil {
			t.Errorf("Error retrieving keys")
		}
		for k, v := range res {
			if !v {
				t.Errorf("recived false for key:%s, but should exist (true)", k)
			}
		}
	})
	t.Run("Bad keys", func(t *testing.T) {
		nonexistentKey := "wreck_it_ralph"
		res, err := env.cash.Exists(env.ctx, nonexistentKey)
		if err != nil {
			t.Fatalf("Error checking for existence of non-existent key:%s", err)
		}
		if res[nonexistentKey] {
			t.Errorf("key should return false not true")
		}

		res, err = env.cash.Exists(env.ctx, "", "", "")
		if err == nil {
			t.Errorf("expected to received error for blank keys, received nil ")
		}
		if res != nil {
			t.Errorf("res should not have a value, should be nil")
		}

		res, err = env.cash.Exists(env.ctx, "")
		if err == nil {
			t.Errorf("expected to received error for blank key, received nil ")
		}
		if res != nil {
			t.Errorf("res should not have a value, should be nil")
		}
	})
}
func testDeleteKeys(t *testing.T, env *testEnv) {
	t.Helper()
	t.Run("Delete key", func(t *testing.T) {
		testKey := "testkey"
		testValue := "testValue"
		err := env.cash.Set(env.ctx, testKey, testValue, time.Minute)
		if err != nil {
			t.Fatalf("Set failed: %s", err)
		}
		res, err := env.cash.Exists(env.ctx, testKey)
		if err != nil {
			t.Fatalf("Error checking for existence of test key:%s", err)
		}
		if res[testKey] == false {
			t.Fatalf("key:%s doesn't exist in cache", testKey)
		}

		err = env.cash.DeleteMultipleKeys(env.ctx, testKey)
		if err != nil {
			t.Errorf("something went wrong with deleting key:%s", testKey)
		}
		err = env.cash.Get(env.ctx, testKey, &res)
		if err == nil {
			t.Errorf("expected error as key:%s should not exist", testKey)
		}
	})

	t.Run("Delete multiple keys", func(t *testing.T) {

		keys := []struct {
			Key   string
			Value any
		}{
			{Key: "k1", Value: "v1"},
			{Key: "k2", Value: 42},
			{Key: "k3", Value: true},
			{Key: "k4", Value: []int{1, 2, 3}},
		}
		keyLimePie := make([]string, 0, len(keys))
		for _, v := range keys {
			err := env.cash.Set(env.ctx, v.Key, v.Value, 4*time.Minute)
			if err != nil {
				t.Fatalf("Error setting values, this should not happen")
			}
			keyLimePie = append(keyLimePie, v.Key)
		}
		res, err := env.cash.Exists(env.ctx, keyLimePie...)
		if err != nil {
			t.Fatalf("Error checking for existence of test key:%s", err)
		}
		for k, v := range res {
			if !v {
				t.Fatalf("this key:%s does not exist", k)
			}
		}

		nonExistentKey := "this_key_doesnt_exist" + time.Now().String()
		res, err = env.cash.Exists(env.ctx, nonExistentKey)
		if res[nonExistentKey] == true {
			t.Fatalf("this key:%s should not exist", nonExistentKey)
		}

		keyLimePie = append(keyLimePie, nonExistentKey)
		err = env.cash.DeleteMultipleKeys(env.ctx, keyLimePie...)
		if err != nil {
			t.Errorf("failure in deleting keys")
		}
		for _, v := range keys {
			var res any
			err = env.cash.Get(env.ctx, v.Key, &res)
			if err == nil {
				t.Errorf("received nil, expected an error")
			}
		}
	})
	t.Run("Empty Keys", func(t *testing.T) {
		err := env.cash.DeleteMultipleKeys(env.ctx, "", "", "", "", "")
		if err == nil {
			t.Errorf("should receive an error, received nil")
		}
	})
}

func runTestWithAllImages(t *testing.T, testFunc func(t *testing.T, env *testEnv)) {

	var wg sync.WaitGroup
	containerRequests := make([]testcontainers.GenericContainerRequest, len(RedisTestImages))
	for i, tc := range RedisTestImages {
		validName := strings.ReplaceAll(tc.name, " ", "_")
		validName = regexp.MustCompile(`[^a-zA-Z0-9_.-]`).ReplaceAllString(validName, "")
		timestamp := time.Now().UnixNano()

		containerRequests[i] = testcontainers.GenericContainerRequest{
			ContainerRequest: testcontainers.ContainerRequest{
				Image:        tc.image,
				ExposedPorts: []string{"6379/tcp"},
				WaitingFor:   wait.ForLog("Ready to accept connections"),
				Name:         fmt.Sprintf("%s_cache_test_%d", validName, timestamp),
			},
		}

	}
	results, err := testcontainers.ParallelContainers(
		context.Background(),
		containerRequests,
		testcontainers.ParallelContainersOptions{WorkersCount: 10},
	)
	if err != nil {
		t.Fatal(err)
	}
	orderChan := make(chan string, len(results))
	for _, container := range results {
		wg.Add(1)
		go func(c testcontainers.Container) {
			defer wg.Done()
			err := container.Start(context.Background())
			if err != nil {
				t.Errorf("Bro why you no start")
				return
			}
			if container.IsRunning() {
				t.Log("Oh yea I am alive")
			} else if !container.IsRunning() {
				t.Errorf("container not running, fold up")
				return
			}

			name, err := container.Name(context.Background())
			if err != nil {
				t.Error("error retrieving name")
				return
			}

			t.Run(strings.TrimPrefix(name, "/"), func(t *testing.T) {
				e := setupTestEnvFromContainer(t, container)
				t.Cleanup(func() {
					teardownTestEnv(t, e)
				})
				testFunc(t, e)
			})
			orderChan <- name
		}(container)
	}

	wg.Wait()
	close(orderChan)
	for res := range orderChan {
		t.Logf("%s%s has completed%s", strings.Repeat("***", 2), res, strings.Repeat("***", 2))
	}
}

func TestSet(t *testing.T) {
	runTestWithAllImages(t, testSet)
}
func TestGetNonExistent(t *testing.T) {
	runTestWithAllImages(t, testGetNonExistent)
}
func TestGetOnMissing(t *testing.T) {
	runTestWithAllImages(t, testGetOnMissing)
}
func TestGetOnMissingWithSingleFlight(t *testing.T) {
	runTestWithAllImages(t, testGetOnMissingWithSingleFlight)
}
func TestDeleteKeys(t *testing.T) {
	runTestWithAllImages(t, testDeleteKeys)
}
func TestExists(t *testing.T) {
	runTestWithAllImages(t, testExists)
}

/*If you haven't already, consider adding tests for concurrent access to your cache.
You could add some chaos testing (e.g., simulating network issues) to ensure your cache behaves correctly under adverse conditions.*/
