package acpruntime

// quarantineTurn revokes reuse before completing the caller's cancelled wait.
// Closing the transport cancels reverse requests and interrupts pending writes;
// cleanup remains owned by this handle until disposal succeeds.
func (d *acpSessionDriver) quarantineTurn(active *activeTurn) {
	d.retireTurnTransport(active, "tainted")
}

func (d *acpSessionDriver) retireTurnTransport(active *activeTurn, status string) {
	d.mu.Lock()
	if d.currentTurn != active {
		d.mu.Unlock()
		return
	}
	if d.status != "closed" {
		d.status = status
	}
	if d.diagnostics.Raw == nil {
		d.diagnostics.Raw = map[string]any{}
	}
	d.diagnostics.Raw["cleanupPending"] = true
	d.mu.Unlock()
	if d.connection != nil && d.connection.peer != nil {
		d.connection.peer.Close()
	}
	go func() {
		d.cleanupMu.Lock()
		defer d.cleanupMu.Unlock()
		if d.disposed {
			d.recordCleanupResult(nil)
			return
		}
		err := runSessionCleanup(d.dispose)
		if err == nil {
			d.disposed = true
		}
		d.recordCleanupResult(err)
		if err != nil {
			d.emitHookSession(RuntimeSessionEvent{Type: "cleanup_failed", SessionID: d.sessionID, AgentType: d.agent.Type, Err: err})
		}
	}()
}

func (d *acpSessionDriver) recordCleanupResult(err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.diagnostics.Raw == nil {
		d.diagnostics.Raw = map[string]any{}
	}
	d.diagnostics.Raw["cleanupPending"] = err != nil
	if err != nil {
		d.diagnostics.Raw["cleanupError"] = err.Error()
	} else {
		delete(d.diagnostics.Raw, "cleanupError")
	}
}

// Host callbacks are bounded and serialized separately from protocol IO. A
// slow callback can lose telemetry, never hold cancellation or terminalization.
func (d *acpSessionDriver) enqueueHook(fn func()) {
	d.hookMu.Lock()
	defer d.hookMu.Unlock()
	if len(d.hookQueue) >= 128 {
		return
	}
	d.hookQueue = append(d.hookQueue, fn)
	if d.hookRunning {
		return
	}
	d.hookRunning = true
	go func() {
		for {
			d.hookMu.Lock()
			if len(d.hookQueue) == 0 {
				d.hookRunning = false
				d.hookMu.Unlock()
				return
			}
			work := d.hookQueue[0]
			d.hookQueue[0] = nil
			d.hookQueue = d.hookQueue[1:]
			d.hookMu.Unlock()
			work()
		}
	}()
}

func (d *acpSessionDriver) interactionLease(sessionID string, requestScoped bool, requireTurn bool) func() bool {
	d.mu.RLock()
	active := d.currentTurn
	allowed := d.status != "closed" && d.status != "tainted" && d.status != "cancelling" && (requestScoped || (sessionID == d.sessionID && (!requireTurn || active != nil && !active.remoteTerminal)))
	d.mu.RUnlock()
	return func() bool {
		d.mu.RLock()
		defer d.mu.RUnlock()
		return allowed && d.status != "closed" && d.status != "tainted" && d.status != "cancelling" && (requestScoped || d.currentTurn == active && (active == nil || !active.remoteTerminal))
	}
}
