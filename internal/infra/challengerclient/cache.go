package challengerclient

import (
	"sync"
	"time"
)

// challengeCache is a small TTL cache for GetChallenge; bounded so an unexpected id spread can't grow it.
type challengeCache struct {
	mu      sync.Mutex
	ttl     time.Duration
	max     int
	entries map[string]cacheEntry
}

type cacheEntry struct {
	challenge Challenge
	expires   time.Time
}

func newChallengeCache(ttl time.Duration, maxEntries int) *challengeCache {
	return &challengeCache{ttl: ttl, max: maxEntries, entries: map[string]cacheEntry{}}
}

func (c *challengeCache) get(id string, now time.Time) (Challenge, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[id]
	if !ok || !now.Before(e.expires) {
		return Challenge{}, false
	}
	return e.challenge, true
}

// set stores ch; when full it drops expired entries first, then arbitrary ones.
func (c *challengeCache) set(id string, ch Challenge, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.entries[id]; !ok && len(c.entries) >= c.max {
		for k, e := range c.entries {
			if !now.Before(e.expires) {
				delete(c.entries, k)
			}
		}
		for k := range c.entries {
			if len(c.entries) < c.max {
				break
			}
			delete(c.entries, k)
		}
	}
	c.entries[id] = cacheEntry{challenge: ch, expires: now.Add(c.ttl)}
}

func (c *challengeCache) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}
