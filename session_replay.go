package acpruntime

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
)

const maxBootstrapReplayBytes = 4 * 1024 * 1024

// sessionReplayBuffer captures load/resume history emitted before the method
// response. It hands off atomically to the driver, preserving wire order.
type sessionReplayBuffer struct {
	mu       sync.Mutex
	pending  []SessionNotification
	target   func(SessionNotification)
	bytes    int
	overflow bool
}

func (b *sessionReplayBuffer) receive(notification SessionNotification) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.target != nil {
		b.target(notification)
		return
	}
	data, err := json.Marshal(notification)
	if err != nil || len(b.pending) >= DefaultMaxThreadEntries || len(data) > maxBootstrapReplayBytes-b.bytes {
		b.overflow = true
		return
	}
	b.bytes += len(data)
	b.pending = append(b.pending, cloneOwned(notification))
}
func (b *sessionReplayBuffer) attach(driver *acpSessionDriver, response NewSessionResponse) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.overflow {
		b.pending = nil
		return errors.New("pre-response session history exceeded the bounded replay budget")
	}
	for _, notification := range b.pending {
		// Prefer a newer explicit method-response snapshot. If the response omits
		// that category, preserve the replayed state (load/resume may return {}).
		if notification.Update.SessionUpdate == "config_option_update" && response.ConfigOptions != nil {
			continue
		}
		if notification.Update.SessionUpdate == "current_mode_update" && response.Modes != nil {
			continue
		}
		driver.handleReplayUpdate(notification)
	}
	b.pending = nil
	b.bytes = 0
	b.target = driver.handleSessionUpdate
	return nil
}

func (d *acpSessionDriver) handleReplayUpdate(notification SessionNotification) {
	if notification.SessionID != "" && notification.SessionID != d.sessionID {
		return
	}
	role := ""
	switch notification.Update.SessionUpdate {
	case "user_message_chunk":
		role = "user_message"
	case "agent_message_chunk", "agent_message", "message":
		role = "assistant_message"
	}
	if role != "" {
		d.mu.Lock()
		text := sessionUpdateText(notification.Update)
		now := time.Now()
		if len(d.thread) > 0 && d.thread[len(d.thread)-1].Kind == role && (notification.Update.MessageID == "" || notification.Update.MessageID == d.replayMessageID) {
			last := &d.thread[len(d.thread)-1]
			last.Text += text
			last.UpdatedAt = now
		} else {
			d.replaySeq++
			d.thread = append(d.thread, ThreadEntry{ID: fmt.Sprintf("replay-%d", d.replaySeq), Kind: role, Status: "completed", Text: text, CreatedAt: now, UpdatedAt: now})
			d.pruneThreadLocked()
		}
		d.replayMessageID = notification.Update.MessageID
		d.mu.Unlock()
	}
	d.handleSessionUpdate(notification)
}
