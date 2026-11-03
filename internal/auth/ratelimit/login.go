package ratelimit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"time"

	"github.com/redis/go-redis/v9"
)

var script = redis.NewScript(`
local ip = redis.call('INCR', KEYS[1])
if ip == 1 then redis.call('PEXPIRE', KEYS[1], ARGV[1]) end
local account = redis.call('INCR', KEYS[2])
if account == 1 then redis.call('PEXPIRE', KEYS[2], ARGV[1]) end
if ip > tonumber(ARGV[2]) or account > tonumber(ARGV[3]) then return 0 end
return 1
`)

type Login struct {
	client                *redis.Client
	window                time.Duration
	ipLimit, accountLimit int
}

func New(client *redis.Client, window time.Duration, ipLimit, accountLimit int) *Login {
	return &Login{client: client, window: window, ipLimit: ipLimit, accountLimit: accountLimit}
}
func digest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
func (l *Login) Allow(ctx context.Context, username, ip string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	keys := []string{"aws-panel:login:ip:" + digest(ip), "aws-panel:login:account:" + digest(username)}
	value, err := script.Run(ctx, l.client, keys, l.window.Milliseconds(), l.ipLimit, l.accountLimit).Int()
	return value == 1, err
}
