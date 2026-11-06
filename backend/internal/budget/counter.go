package budget

import (
	"context"
	"log/slog"
	"math/rand/v2"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/mydisha/keirouter/backend/internal/store"
)

// CounterValue is one shared spend counter.
type CounterValue struct {
	Found     bool
	CostNanos int64
	Tokens    int64
}

// Delta is spend to add to one counter.
type Delta struct {
	Key       string
	CostNanos int64
	Tokens    int64
}

// Counter holds per-scope, per-period spend shared by every replica, so a
// budget check is a key lookup instead of a SUM over the usage table, and
// all replicas agree on the spend.
//
// Counters are a cache of the usage table: a missing counter is seeded from
// it, and each counter expires after a TTL and is seeded again, which bounds
// any drift from increments lost to a crash or a full queue.
//
// Seeding is two steps so no spend falls between the table read and the
// counter: BeginSeed creates an empty counter that already collects
// increments, the usage table is read, and Seed adds that reading. Increments
// are only sent for rows already in the table, so a row is either in the
// reading, or arrives as an increment after BeginSeed, or both (counted
// twice until the next re-seed, the safe direction for a spend limit).
type Counter interface {
	// Load returns the seeded counters; a counter still being seeded reads
	// as not found.
	Load(ctx context.Context, keys []string) ([]CounterValue, error)
	// BeginSeed creates an empty, unseeded counter for each key that has
	// none, expiring after ttl unless Seed completes it. It returns each
	// key's counter token: a reading taken after BeginSeed is valid only for
	// the counter with that token.
	BeginSeed(ctx context.Context, keys []string, ttl time.Duration) ([]string, error)
	// Seed adds v to the counter with the given token and marks it seeded
	// with the given ttl. It does nothing if that counter is already seeded,
	// gone, or was replaced by a newer one.
	Seed(ctx context.Context, key, token string, v CounterValue, ttl time.Duration) error
	// Add increments counters that exist; deltas for missing ones are dropped
	// because the next seed reads them from the usage table.
	Add(ctx context.Context, deltas []Delta) error
	// PublishInvalidate tells every replica that budget definitions changed.
	PublishInvalidate(ctx context.Context) error
	// SubscribeInvalidate calls fn whenever a replica publishes, until ctx ends.
	SubscribeInvalidate(ctx context.Context, fn func())
}

// counterKey names the counter of one scope for the period starting at
// since. The {scope} hash tag keeps a scope's counters on one cluster slot.
func counterKey(prefix string, kind store.BudgetScope, scopeID string, since time.Time) string {
	period := "total"
	if !since.IsZero() {
		period = strconv.FormatInt(since.Unix(), 10)
	}
	return prefix + "{" + string(kind) + ":" + scopeID + "}:" + period
}

// A counter is a hash: "c" cost nanos, "t" tokens, "g" the token BeginSeed
// gave it, and "s" once seeded.
var beginSeedScript = redis.NewScript(`
local g = redis.call('HGET', KEYS[1], 'g')
if g then
  return g
end
redis.call('DEL', KEYS[1])
redis.call('HSET', KEYS[1], 'c', 0, 't', 0, 'g', ARGV[2])
redis.call('PEXPIRE', KEYS[1], ARGV[1])
return ARGV[2]
`)

var seedScript = redis.NewScript(`
if redis.call('HGET', KEYS[1], 'g') ~= ARGV[4] or redis.call('HEXISTS', KEYS[1], 's') == 1 then
  return 0
end
redis.call('HINCRBY', KEYS[1], 'c', ARGV[1])
redis.call('HINCRBY', KEYS[1], 't', ARGV[2])
redis.call('HSET', KEYS[1], 's', 1)
redis.call('PEXPIRE', KEYS[1], ARGV[3])
return 1
`)

var addScript = redis.NewScript(`
if redis.call('EXISTS', KEYS[1]) == 0 then
  return 0
end
redis.call('HINCRBY', KEYS[1], 'c', ARGV[1])
redis.call('HINCRBY', KEYS[1], 't', ARGV[2])
return 1
`)

// RedisCounter is a Counter on redis hashes ("c" = cost nanos, "t" = tokens).
type RedisCounter struct {
	client  redis.UniversalClient
	timeout time.Duration
	channel string
	log     *slog.Logger
}

// NewRedisCounter builds a RedisCounter. timeout bounds each call on the
// request path.
func NewRedisCounter(client redis.UniversalClient, timeout time.Duration, log *slog.Logger) *RedisCounter {
	if timeout <= 0 {
		timeout = 100 * time.Millisecond
	}
	if log == nil {
		log = slog.Default()
	}
	return &RedisCounter{client: client, timeout: timeout, channel: "kr:bud:invalidate", log: log}
}

// Load reads every key in one pipelined round trip.
func (c *RedisCounter) Load(ctx context.Context, keys []string) ([]CounterValue, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	cmds := make([]*redis.SliceCmd, len(keys))
	_, err := c.client.Pipelined(ctx, func(p redis.Pipeliner) error {
		for i, k := range keys {
			cmds[i] = p.HMGet(ctx, k, "c", "t", "s")
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	out := make([]CounterValue, len(keys))
	for i, cmd := range cmds {
		vals := cmd.Val()
		if len(vals) != 3 || vals[0] == nil || vals[2] == nil {
			continue
		}
		out[i].Found = true
		out[i].CostNanos = parseRedisInt(vals[0])
		out[i].Tokens = parseRedisInt(vals[1])
	}
	return out, nil
}

func parseRedisInt(v any) int64 {
	s, ok := v.(string)
	if !ok {
		return 0
	}
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}

// BeginSeed implements Counter in one pipelined round trip.
func (c *RedisCounter) BeginSeed(ctx context.Context, keys []string, ttl time.Duration) ([]string, error) {
	if len(keys) == 0 {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	if err := beginSeedScript.Load(ctx, c.client).Err(); err != nil {
		return nil, err
	}
	cmds := make([]*redis.Cmd, len(keys))
	_, err := c.client.Pipelined(ctx, func(p redis.Pipeliner) error {
		for i, k := range keys {
			cmds[i] = beginSeedScript.EvalSha(ctx, p, []string{k}, ttl.Milliseconds(), newSeedToken())
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	tokens := make([]string, len(keys))
	for i, cmd := range cmds {
		if tokens[i], err = cmd.Text(); err != nil {
			return nil, err
		}
	}
	return tokens, nil
}

func newSeedToken() string {
	return strconv.FormatUint(rand.Uint64(), 36) + strconv.FormatUint(rand.Uint64(), 36)
}

// Seed implements Counter. The TTL gets ±10% jitter so counters seeded
// together do not all expire, and hit the database, at the same moment.
func (c *RedisCounter) Seed(ctx context.Context, key, token string, v CounterValue, ttl time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	jitter := time.Duration(rand.Int64N(int64(ttl)/5+1)) - ttl/10
	return seedScript.Run(ctx, c.client, []string{key}, v.CostNanos, v.Tokens, (ttl + jitter).Milliseconds(), token).Err()
}

// Add implements Counter in one pipelined round trip.
func (c *RedisCounter) Add(ctx context.Context, deltas []Delta) error {
	if len(deltas) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	// Make sure the script is cached so the pipelined EVALSHAs succeed.
	if err := addScript.Load(ctx, c.client).Err(); err != nil {
		return err
	}
	_, err := c.client.Pipelined(ctx, func(p redis.Pipeliner) error {
		for _, d := range deltas {
			addScript.EvalSha(ctx, p, []string{d.Key}, d.CostNanos, d.Tokens)
		}
		return nil
	})
	return err
}

// PublishInvalidate implements Counter.
func (c *RedisCounter) PublishInvalidate(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	return c.client.Publish(ctx, c.channel, "1").Err()
}

// SubscribeInvalidate implements Counter. go-redis resubscribes after a
// dropped connection; a replica that misses a message still converges when
// its 10s definition cache expires.
func (c *RedisCounter) SubscribeInvalidate(ctx context.Context, fn func()) {
	sub := c.client.Subscribe(ctx, c.channel)
	go func() {
		defer sub.Close()
		ch := sub.Channel()
		for {
			select {
			case <-ctx.Done():
				return
			case _, ok := <-ch:
				if !ok {
					return
				}
				fn()
			}
		}
	}()
}
