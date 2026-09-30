package acpruntime

import (
	"context"
	"time"
)

const defaultAuthorityTimeout = 2 * time.Minute
const maxConcurrentAuthorityCalls = 32

// Authority callbacks must honor cancellation. A bounded slot remains occupied
// until a callback returns, so an uncooperative host cannot create unbounded
// callback goroutines by repeatedly cancelling requests.
func runAuthority[T any](c *Connection, ctx context.Context, sessionID string, timeout time.Duration, fn func(Context) (T, error)) (T, error) {
	var zero T
	if timeout <= 0 {
		timeout = defaultAuthorityTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	c.stateMu.Lock()
	c.nextAuthorityID++
	id := c.nextAuthorityID
	c.authorityCancels[id] = authorityCancellation{sessionID: sessionID, cancel: cancel}
	c.stateMu.Unlock()
	defer func() { cancel(); c.stateMu.Lock(); delete(c.authorityCancels, id); c.stateMu.Unlock() }()
	go func() {
		select {
		case <-ctx.Done():
		case <-c.peer.Done():
			cancel()
		}
	}()
	if ctx.Err() != nil {
		return zero, ctx.Err()
	}
	select {
	case c.authoritySlots <- struct{}{}:
	case <-ctx.Done():
		return zero, ctx.Err()
	}
	type result struct {
		value T
		err   error
	}
	ch := make(chan result, 1)
	go func() { defer func() { <-c.authoritySlots }(); v, err := fn(ctx); ch <- result{v, err} }()
	select {
	case r := <-ch:
		if ctx.Err() != nil {
			return zero, ctx.Err()
		}
		return r.value, r.err
	case <-ctx.Done():
		return zero, ctx.Err()
	}
}

type authorityCancellation struct {
	sessionID string
	cancel    context.CancelFunc
}

func (c *Connection) cancelSessionAuthorities(sessionID string) {
	c.stateMu.RLock()
	var cancels []context.CancelFunc
	for _, entry := range c.authorityCancels {
		if entry.sessionID == sessionID {
			cancels = append(cancels, entry.cancel)
		}
	}
	c.stateMu.RUnlock()
	for _, cancel := range cancels {
		cancel()
	}
	if c.elicitation != nil {
		c.elicitation.cancelSession(sessionID)
	}
}
