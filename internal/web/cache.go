package web

import (
	"container/list"
	"context"
	"sync"
	"time"
)

// lru is a small least-recently-used cache. Concurrent misses for one key share
// a single fill, and fills run detached from the visitor's request so a client
// hanging up can't waste node work.
type lru struct {
	max int

	mu       sync.Mutex
	ll       *list.List
	items    map[string]*list.Element
	inflight map[string]*fill
}

type entry struct {
	key string
	at  time.Time
	val any
}

type fill struct {
	done chan struct{}
	val  any
	err  error
}

func newLRU(max int) *lru {
	return &lru{max: max, ll: list.New(), items: map[string]*list.Element{}, inflight: map[string]*fill{}}
}

// get returns the cached value for key if younger than ttl, otherwise runs f
// (once, however many callers are waiting). When f fails, a stale value is
// returned if there is one.
func (c *lru) get(ctx context.Context, key string, ttl time.Duration, f func(context.Context) (any, error)) (any, error) {
	c.mu.Lock()
	var stale any
	if el, ok := c.items[key]; ok {
		e := el.Value.(*entry)
		if time.Since(e.at) < ttl {
			c.ll.MoveToFront(el)
			c.mu.Unlock()
			return e.val, nil
		}
		stale = e.val
	}
	fl, running := c.inflight[key]
	if !running {
		fl = &fill{done: make(chan struct{})}
		c.inflight[key] = fl
		go func() {
			fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
			defer cancel()
			fl.val, fl.err = f(fctx)
			c.mu.Lock()
			delete(c.inflight, key)
			if fl.err == nil {
				c.put(key, fl.val)
			}
			c.mu.Unlock()
			close(fl.done)
		}()
	}
	c.mu.Unlock()
	select {
	case <-fl.done:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if fl.err != nil {
		if stale != nil {
			return stale, nil
		}
		return nil, fl.err
	}
	return fl.val, nil
}

// put stores val. Caller holds mu.
func (c *lru) put(key string, val any) {
	if el, ok := c.items[key]; ok {
		el.Value = &entry{key, time.Now(), val}
		c.ll.MoveToFront(el)
		return
	}
	c.items[key] = c.ll.PushFront(&entry{key, time.Now(), val})
	for c.ll.Len() > c.max {
		old := c.ll.Back()
		c.ll.Remove(old)
		delete(c.items, old.Value.(*entry).key)
	}
}
