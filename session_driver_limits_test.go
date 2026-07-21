package acpruntime

import (
	"testing"
	"time"
)

func TestReadModelThreadPruneKeepsRecentWindow(t *testing.T) {
	driver := &acpSessionDriver{
		status:    "ready",
		maxThread: 3,
		thread:    nil,
	}
	for i := 0; i < 5; i++ {
		driver.thread = append(driver.thread, ThreadEntry{
			ID:        string(rune('a' + i)),
			Kind:      "user_message",
			Status:    "completed",
			Text:      "x",
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		})
		driver.pruneThreadLocked()
	}
	if len(driver.thread) != 3 {
		t.Fatalf("thread len = %d, want 3", len(driver.thread))
	}
	// Most recent three should remain: c,d,e (indices 2,3,4 from a..e)
	if driver.thread[0].ID != "c" || driver.thread[2].ID != "e" {
		t.Fatalf("thread window = %#v, want trailing c,d,e", driver.thread)
	}
}

func TestReadModelToolCallPruneDropsCompletedFirst(t *testing.T) {
	driver := &acpSessionDriver{
		status:       "ready",
		maxToolCalls: 1,
		toolCalls:    map[string]ToolCallSnapshot{},
		operations:   map[string]Operation{},
	}
	driver.toolCalls["old"] = ToolCallSnapshot{ID: "old", Status: "completed"}
	driver.operations["old"] = Operation{ID: "old", Phase: "completed"}
	driver.toolCalls["live"] = ToolCallSnapshot{ID: "live", Status: "in_progress"}
	driver.operations["live"] = Operation{ID: "live", Phase: "running"}
	driver.pruneToolCallsLocked()
	if _, ok := driver.toolCalls["old"]; ok {
		t.Fatal("completed tool call was not pruned")
	}
	if _, ok := driver.toolCalls["live"]; !ok {
		t.Fatal("in-progress tool call was pruned")
	}
}

func TestResolveReadModelLimitsDefaults(t *testing.T) {
	thread, tools, perms := resolveReadModelLimits(ReadModelLimits{})
	if thread != DefaultMaxThreadEntries || tools != DefaultMaxToolCallEntries || perms != DefaultMaxPermissionEntries {
		t.Fatalf("defaults = %d/%d/%d, want %d/%d/%d", thread, tools, perms, DefaultMaxThreadEntries, DefaultMaxToolCallEntries, DefaultMaxPermissionEntries)
	}
	thread, tools, perms = resolveReadModelLimits(ReadModelLimits{MaxThreadEntries: -1, MaxToolCallEntries: 10, MaxPermissionEntries: 2})
	if thread != -1 || tools != 10 || perms != 2 {
		t.Fatalf("custom limits = %d/%d/%d", thread, tools, perms)
	}
}
