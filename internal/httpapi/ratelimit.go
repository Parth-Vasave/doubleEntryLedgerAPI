package httpapi

import (
	"math"
	"strconv"
	"sync"

	"golang.org/x/time/rate"
)

// rateLimiter is a per-principal token bucket. It is per instance: with N API
// replicas the effective limit is N times higher, which is acceptable for
// abuse protection (a shared limiter would need Redis or similar).
type rateLimiter struct {
	rps   rate.Limit
	burst int

	mu      sync.Mutex
	buckets map[string]*rate.Limiter
}

func newRateLimiter(rps float64, burst int) *rateLimiter {
	if rps <= 0 {
		return nil
	}
	return &rateLimiter{rps: rate.Limit(rps), burst: burst, buckets: map[string]*rate.Limiter{}}
}

// allow reports whether key may proceed and, if not, seconds until it may.
func (l *rateLimiter) allow(key string) (bool, string) {
	if l == nil {
		return true, ""
	}
	l.mu.Lock()
	b, ok := l.buckets[key]
	if !ok {
		b = rate.NewLimiter(l.rps, l.burst)
		l.buckets[key] = b
	}
	l.mu.Unlock()
	res := b.Reserve()
	if d := res.Delay(); d > 0 {
		res.Cancel()
		return false, strconv.Itoa(int(math.Ceil(d.Seconds())))
	}
	return true, ""
}
