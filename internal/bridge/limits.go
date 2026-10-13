package bridge

import (
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// idleAfter is how long a topic's limiter is kept without a request.
const idleAfter = 10 * time.Minute

// topicLimiter holds a token bucket per request topic, at most max of
// them. When it is full it forgets the topics idle longest; a new topic
// that still finds no room is refused, so the map never grows without
// bound.
type topicLimiter struct {
	mu    sync.Mutex
	every rate.Limit
	burst int
	max   int
	now   func() time.Time
	m     map[string]*entry
}

type entry struct {
	lim  *rate.Limiter
	seen time.Time
}

func newTopicLimiter(perSecond float64, burst, max int) *topicLimiter {
	return &topicLimiter{every: rate.Limit(perSecond), burst: burst, max: max, now: time.Now, m: map[string]*entry{}}
}

// allow takes a token for the topic. full is true when it was refused
// because no more topics fit.
func (t *topicLimiter) allow(topic string) (ok, full bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	e := t.m[topic]
	if e == nil {
		if len(t.m) >= t.max {
			for k, v := range t.m {
				if now.Sub(v.seen) > idleAfter {
					delete(t.m, k)
				}
			}
			if len(t.m) >= t.max {
				return false, true
			}
		}
		e = &entry{lim: rate.NewLimiter(t.every, t.burst)}
		t.m[topic] = e
	}
	e.seen = now
	return e.lim.AllowN(now, 1), false
}
