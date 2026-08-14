//go:build cc

package acpruntime

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func TestClaudeCodeOrphanFollowUpLive(t *testing.T) {
	if os.Getenv("RUN_CC") == "0" {
		t.Skip("RUN_CC=0")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cwd := t.TempDir()
	runtime := NewRuntime(NewStdioConnectionFactory(StdioFactoryOptions{}), RuntimeOptions{})
	session, err := runtime.StartSession(ctx, StartSessionOptions{
		Agent:         CreateClaudeCodeAgent(Agent{}),
		CWD:           cwd,
		InitialConfig: InitialConfig{Mode: "yolo"},
		Handlers:      AuthorityHandlers{Permission: allowAllPermission},
	})
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	defer session.Close(context.Background())

	updates := session.Updates()
	if updates == nil {
		t.Fatal("Session.Updates() is nil")
	}
	type collected struct {
		events []SessionNotification
	}
	got := make(chan collected, 1)
	go func() {
		var events []SessionNotification
		idle := time.NewTimer(90 * time.Second)
		defer idle.Stop()
		for {
			select {
			case <-ctx.Done():
				got <- collected{events: events}
				return
			case <-idle.C:
				got <- collected{events: events}
				return
			case notification, ok := <-updates:
				if !ok {
					got <- collected{events: events}
					return
				}
				if notification.UpdateID == "" {
					continue
				}
				events = append(events, notification)
				t.Logf("orphan type=%s id=%s terminal=%v text=%q",
					notification.Update.SessionUpdate, notification.UpdateID, notification.Terminal, previewLive(sessionUpdateText(notification.Update)))
				if notification.Terminal {
					// Keep listening briefly for a trailing closer-only event, but
					// a terminal is enough to stop the idle clock from running out.
					if !idle.Stop() {
						select {
						case <-idle.C:
						default:
						}
					}
					idle.Reset(2 * time.Second)
				}
			}
		}
	}()

	prompt := strings.Join([]string{
		"Start this exact command in the background and do not wait for it:",
		"sleep 60 && echo ORPHAN-BG-DONE-42",
		"As soon as it is running in the background, reply with the single line STARTED and end your turn.",
		"Do not poll, do not wait, do not summarize after it finishes in this turn.",
	}, "\n")
	started := time.Now()
	completion, err := session.Run(ctx, prompt)
	t.Logf("prompt settled after %s stop=%s err=%v output=%q", time.Since(started), completion.StopReason, err, previewLive(completion.OutputText))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	result := <-got
	if len(result.events) == 0 {
		t.Fatal("no orphan Updates() after Claude background task")
	}
	var text strings.Builder
	ids := map[string]int{}
	var sawTerminal bool
	for _, event := range result.events {
		ids[event.UpdateID]++
		if event.Terminal {
			sawTerminal = true
		}
		if event.Update.SessionUpdate == "agent_message_chunk" {
			text.WriteString(sessionUpdateText(event.Update))
		}
	}
	t.Logf("orphan events=%d generations=%d terminal=%v text=%q", len(result.events), len(ids), sawTerminal, previewLive(text.String()))
	if !strings.Contains(text.String(), "ORPHAN-BG-DONE-42") && !strings.Contains(text.String(), "ORPHAN") {
		t.Fatalf("follow-up text missing sentinel; events=%d text=%q", len(result.events), previewLive(text.String()))
	}
}

func previewLive(text string) string {
	text = strings.ReplaceAll(text, "\n", "\\n")
	if len(text) > 240 {
		return text[:240] + "..."
	}
	return text
}
