package acpruntime

import (
	"context"
	"sync"
)

// contextLock serializes cleanup while allowing a second caller to abandon its
// wait without interrupting the cleanup owner or forgetting its resources.
type contextLock struct {
	once  sync.Once
	token chan struct{}
}

func (m *contextLock) init() { m.once.Do(func() { m.token = make(chan struct{}, 1) }) }
func (m *contextLock) LockContext(ctx context.Context) error {
	m.init()
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case m.token <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (m *contextLock) Lock()   { _ = m.LockContext(context.Background()) }
func (m *contextLock) Unlock() { <-m.token }
