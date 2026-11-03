package ratelimit

import (
	"context"
	"net"
	"os"
	"testing"
	"time"

	"github.com/herta-network/aws-panel/internal/auth/token"
	"github.com/redis/go-redis/v9"
)

func TestUnavailableRedisFailsClosed(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	client := redis.NewClient(&redis.Options{Addr: address, MaxRetries: -1, DialTimeout: 200 * time.Millisecond})
	defer client.Close()
	allowed, err := New(client, time.Minute, 3, 2).Allow(context.Background(), "test", "127.0.0.1")
	if err == nil || allowed {
		t.Fatal("limiter allowed a request without Redis")
	}
}

func TestRedisEnforcesBothDimensions(t *testing.T) {
	address := os.Getenv("AWS_PANEL_TEST_REDIS_ADDRESS")
	if address == "" {
		t.Skip("isolated Redis not configured")
	}
	client := redis.NewClient(&redis.Options{Addr: address})
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	id, _ := token.New()
	user, ip := id+"-user", id+"-ip"
	limiter := New(client, time.Minute, 3, 2)
	keys := []string{"aws-panel:login:ip:" + digest(ip), "aws-panel:login:ip:" + digest(ip+"2"),
		"aws-panel:login:account:" + digest(user), "aws-panel:login:account:" + digest(user+"2")}
	defer client.Del(context.Background(), keys...)
	for i, want := range []bool{true, true, false} {
		got, err := limiter.Allow(ctx, user, ip)
		if err != nil || got != want {
			t.Fatalf("attempt %d: allowed=%v err=%v", i, got, err)
		}
	}
	if got, err := limiter.Allow(ctx, user+"2", ip); err != nil || got {
		t.Fatal("IP limit not applied across usernames")
	}
	if got, err := limiter.Allow(ctx, user, ip+"2"); err != nil || got {
		t.Fatal("account limit not applied across sources")
	}
	ttl, err := client.PTTL(ctx, keys[0]).Result()
	if err != nil || ttl <= 0 || ttl > time.Minute {
		t.Fatal("rate window has no bounded expiry")
	}
}
