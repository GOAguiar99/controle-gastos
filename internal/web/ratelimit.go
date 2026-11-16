package web

import (
	"net/http"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// limiter is a small in-memory, per-key token bucket. One VM serves the app,
// so no shared store is needed; idle keys are dropped so memory stays bounded.
type limiter struct {
	mu      sync.Mutex
	every   rate.Limit
	burst   int
	keys    map[string]*limiterEntry
	maxKeys int
	lastGC  time.Time
}

type limiterEntry struct {
	l    *rate.Limiter
	seen time.Time
}

func newLimiter(perMinute, burst int) *limiter {
	return &limiter{every: rate.Limit(float64(perMinute) / 60), burst: burst, keys: map[string]*limiterEntry{}, maxKeys: 50000}
}

func (l *limiter) allow(key string) bool {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	if now.Sub(l.lastGC) > time.Minute || len(l.keys) >= l.maxKeys {
		for k, e := range l.keys {
			if now.Sub(e.seen) > 10*time.Minute {
				delete(l.keys, k)
			}
		}
		l.lastGC = now
		if len(l.keys) >= l.maxKeys { // under a flood of distinct keys, fail closed
			return false
		}
	}
	e, ok := l.keys[key]
	if !ok {
		e = &limiterEntry{l: rate.NewLimiter(l.every, l.burst)}
		l.keys[key] = e
	}
	e.seen = now
	return e.l.Allow()
}

func tooMany(w http.ResponseWriter) {
	w.Header().Set("Retry-After", "60")
	http.Error(w, "Muitas requisições. Aguarde um minuto e tente de novo.", http.StatusTooManyRequests)
}
