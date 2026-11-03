// Package catalogcache caches display data. Cloud mutation planning and execution
// must use their original sources and never this cache.
package catalogcache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/herta-network/aws-panel/internal/catalogue"
	"github.com/redis/go-redis/v9"
)

type Policy struct{ Fresh, Retain time.Duration }

var (
	Products    = Policy{24 * time.Hour, 7 * 24 * time.Hour}
	Images      = Policy{6 * time.Hour, 24 * time.Hour}
	Environment = Policy{2 * time.Minute, 15 * time.Minute}
	Regions     = Policy{10 * time.Minute, time.Hour}
	Resources   = Policy{30 * time.Second, 2 * time.Minute}
	QuotaUsage  = Policy{time.Minute, 5 * time.Minute}
	QuotaLimits = Policy{time.Hour, 24 * time.Hour}
)

const maxEntries = 128
const maxBytes = 512 * 1024

var ErrBusy = errors.New("catalogue refresh capacity exhausted")

type entry struct {
	Data       json.RawMessage `json:"data"`
	Updated    time.Time       `json:"updated"`
	FreshUntil time.Time       `json:"fresh_until"`
	Expires    time.Time       `json:"expires"`
}
type flight struct {
	done     chan struct{}
	value    entry
	err      error
	complete bool
}
type failure struct{ retry time.Time }
type Cache struct {
	ctx      context.Context
	cancel   context.CancelFunc
	redis    *redis.Client
	now      func() time.Time
	mu       sync.Mutex
	entries  map[string]entry
	flights  map[string]*flight
	failures map[string]failure
	closed   bool
	wg       sync.WaitGroup
}

func New(ctx context.Context, client *redis.Client) *Cache {
	ctx, cancel := context.WithCancel(ctx)
	return &Cache{ctx: ctx, cancel: cancel, redis: client, now: time.Now, entries: map[string]entry{}, flights: map[string]*flight{}, failures: map[string]failure{}}
}
func (c *Cache) Close() { c.mu.Lock(); c.closed = true; c.cancel(); c.mu.Unlock(); c.wg.Wait() }
func Key(parts ...string) string {
	raw, _ := json.Marshal(parts)
	sum := sha256.Sum256(raw)
	return "aws-panel:catalogue:v1:" + hex.EncodeToString(sum[:])
}
func (c *Cache) remember(key string, v entry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.entries[key]; !ok && len(c.entries) >= maxEntries {
		oldest := ""
		for k, e := range c.entries {
			if oldest == "" || e.Updated.Before(c.entries[oldest].Updated) {
				oldest = k
			}
		}
		delete(c.entries, oldest)
		delete(c.failures, oldest)
	}
	c.entries[key] = v
}
func (c *Cache) lookup(ctx context.Context, key string) (entry, bool) {
	c.mu.Lock()
	v, ok := c.entries[key]
	c.mu.Unlock()
	now := c.now()
	if ok && now.Before(v.Expires) {
		return v, true
	}
	if c.redis != nil {
		readCtx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
		defer cancel()
		raw, err := c.redis.Get(readCtx, key).Bytes()
		if err == nil && len(raw) <= maxBytes && json.Unmarshal(raw, &v) == nil && len(v.Data) > 0 && !v.Updated.After(now) && v.Updated.Before(v.FreshUntil) && !v.FreshUntil.After(v.Expires) && now.Before(v.Expires) {
			c.remember(key, v)
			return v, true
		}
	}
	return entry{}, false
}

// The Redis namespace is bounded independently of Asynq's queue keys.
var save = redis.NewScript(`
redis.call('SET', KEYS[1], ARGV[1], 'PX', ARGV[2])
redis.call('ZREMRANGEBYSCORE', KEYS[2], '-inf', ARGV[3])
redis.call('ZADD', KEYS[2], ARGV[4], KEYS[1])
local count=redis.call('ZCARD', KEYS[2])
if count>tonumber(ARGV[5]) then
 local old=redis.call('ZRANGE', KEYS[2], 0, count-tonumber(ARGV[5])-1)
 for _,key in ipairs(old) do redis.call('DEL',key);redis.call('ZREM',KEYS[2],key) end
end
redis.call('PEXPIRE', KEYS[2], ARGV[6])
return 1
`)

func (c *Cache) persist(ctx context.Context, key string, v entry) {
	raw, err := json.Marshal(v)
	if err != nil || len(raw) > maxBytes {
		return
	}
	c.remember(key, v)
	if c.redis != nil {
		writeCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
		defer cancel()
		_ = save.Run(writeCtx, c.redis, []string{key, "aws-panel:catalogue:v1:index"}, raw, v.Expires.Sub(c.now()).Milliseconds(), c.now().UnixMilli(), v.Expires.UnixMilli(), maxEntries, (8 * 24 * time.Hour).Milliseconds()).Err()
	}
}
func (c *Cache) start(key string, policy Policy, force bool, load func(context.Context) ([]byte, bool, error)) (*flight, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if f := c.flights[key]; f != nil {
		return f, true
	}
	if c.closed || len(c.flights) >= 32 {
		return nil, false
	}
	if !force && c.now().Before(c.failures[key].retry) {
		return nil, false
	}
	f := &flight{done: make(chan struct{})}
	c.flights[key] = f
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		ctx, cancel := context.WithTimeout(c.ctx, 2*time.Minute)
		defer cancel()
		data, complete, err := load(ctx)
		if err == nil {
			now := c.now()
			f.value = entry{Data: data, Updated: now, FreshUntil: now.Add(policy.Fresh), Expires: now.Add(policy.Retain)}
			if complete {
				c.persist(ctx, key, f.value)
			}
		}
		f.err = err
		f.complete = complete
		c.mu.Lock()
		delete(c.flights, key)
		if err != nil || !complete {
			if len(c.failures) >= maxEntries {
				for k := range c.failures {
					delete(c.failures, k)
					break
				}
			}
			c.failures[key] = failure{retry: c.now().Add(time.Minute)}
		} else {
			delete(c.failures, key)
		}
		close(f.done)
		c.mu.Unlock()
	}()
	return f, true
}
func Read[T any](ctx context.Context, c *Cache, key string, policy Policy, force bool, load func(context.Context) (T, error)) (T, catalogue.Freshness, error) {
	return ReadChecked(ctx, c, key, policy, force, load, func(T) bool { return true })
}

// ReadChecked returns partial observations to the requesting caller, but only
// complete observations may replace a cached snapshot. Background failures keep
// the last complete value until its original retention deadline.
func ReadChecked[T any](ctx context.Context, c *Cache, key string, policy Policy, force bool, load func(context.Context) (T, error), complete func(T) bool) (T, catalogue.Freshness, error) {
	var zero T
	if c == nil {
		v, err := load(ctx)
		now := time.Now()
		return v, catalogue.Freshness{UpdatedAt: now, RefreshAfter: now}, err
	}
	fetch := func(ctx context.Context) ([]byte, bool, error) {
		v, err := load(ctx)
		if err != nil {
			return nil, false, err
		}
		raw, err := json.Marshal(v)
		return raw, complete(v), err
	}
	if !force {
		if v, ok := c.lookup(ctx, key); ok {
			var result T
			if json.Unmarshal(v.Data, &result) == nil {
				stale := !c.now().Before(v.FreshUntil)
				c.mu.Lock()
				_, failed := c.failures[key]
				c.mu.Unlock()
				updating := false
				if stale {
					_, updating = c.start(key, policy, false, fetch)
				}
				return result, catalogue.Freshness{UpdatedAt: v.Updated, RefreshAfter: v.FreshUntil, Cached: true, Stale: stale, Refreshing: updating, RefreshFailed: failed}, nil
			}
		}
	}
	f, ok := c.start(key, policy, true, fetch)
	if !ok {
		return zero, catalogue.Freshness{}, ErrBusy
	}
	select {
	case <-ctx.Done():
		return zero, catalogue.Freshness{}, ctx.Err()
	case <-f.done:
	}
	if f.err != nil {
		return zero, catalogue.Freshness{}, f.err
	}
	if err := json.Unmarshal(f.value.Data, &zero); err != nil {
		return zero, catalogue.Freshness{}, err
	}
	return zero, catalogue.Freshness{UpdatedAt: f.value.Updated, RefreshAfter: f.value.FreshUntil, RefreshFailed: !f.complete}, nil
}
