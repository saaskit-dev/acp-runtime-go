package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	acp "github.com/saaskit-dev/acp-runtime-go"
)

// Turn.Events is a lossy preview. Only Completion is authoritative. Recover a
// missing suffix when the emitted preview remains a prefix; never claim success
// if a missing middle event or rewritten text made that reconstruction unsafe.
func (s *Server) consumeText(ctx context.Context, turn acp.TurnHandle, early *acp.TurnResult, send func(string) error) (acp.TurnCompletion, error) {
	var delivered strings.Builder
	for turn.Events != nil {
		select {
		case <-ctx.Done():
			return acp.TurnCompletion{}, ctx.Err()
		case event, ok := <-turn.Events:
			if !ok {
				turn.Events = nil
				continue
			}
			if event.Type != "text" || event.Text == "" {
				continue
			}
			if len(event.Text) > s.outputLimit()-delivered.Len() {
				return acp.TurnCompletion{}, outputLimitError()
			}
			if err := send(event.Text); err != nil {
				return acp.TurnCompletion{}, err
			}
			delivered.WriteString(event.Text)
		}
	}
	var result acp.TurnResult
	if early != nil {
		result = *early
	} else {
		select {
		case <-ctx.Done():
			return acp.TurnCompletion{}, ctx.Err()
		case value, ok := <-turn.Completion:
			if !ok {
				return acp.TurnCompletion{}, errors.New("turn closed without an authoritative completion")
			}
			result = value
		}
	}
	if result.Err != nil {
		return result.Completion, result.Err
	}
	text := result.Completion.OutputText
	if err := s.checkOutput(text); err != nil {
		return result.Completion, err
	}
	if !strings.HasPrefix(text, delivered.String()) {
		return result.Completion, sessionHTTPError{status: http.StatusBadGateway, code: "stream_gap", message: "stream text is not a prefix of the authoritative result; partial output must not be treated as complete"}
	}
	if suffix := text[delivered.Len():]; suffix != "" {
		if err := send(suffix); err != nil {
			return result.Completion, err
		}
	}
	return result.Completion, nil
}

func outputLimitError() error {
	return sessionHTTPError{status: http.StatusBadGateway, code: "output_limit", message: "ACP output exceeded the gateway text byte limit"}
}

// Check synchronous StartTurn rejection before committing streaming headers.
func startStreamTurn(ctx context.Context, session *acp.Session, prompt string) (acp.TurnHandle, *acp.TurnResult) {
	turn := session.StartTurn(ctx, acp.RuntimePrompt{Text: prompt})
	select {
	case result, ok := <-turn.Completion:
		if !ok {
			result.Err = errors.New("turn closed without an authoritative completion")
		}
		return turn, &result
	default:
		return turn, nil
	}
}

func streamHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
}

func (s *Server) sendStreamFrame(w http.ResponseWriter, event string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	prefix := ""
	if event != "" {
		prefix = "event: " + event + "\n"
	}
	return s.writeStreamFrame(w, prefix+"data: "+string(data)+"\n\n")
}

func (s *Server) writeStreamFrame(w http.ResponseWriter, frame string) error {
	controller := http.NewResponseController(w)
	timeout := s.streamWriteTimeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	if timeout > 0 {
		if err := controller.SetWriteDeadline(time.Now().Add(timeout)); err != nil && !errors.Is(err, http.ErrNotSupported) {
			return err
		}
	}
	if _, err := fmt.Fprint(w, frame); err != nil {
		return err
	}
	return controller.Flush()
}

func (s *Server) streamTurn(ctx context.Context, w http.ResponseWriter, session *acp.Session, prompt string, req chatCompletionRequest, model string) error {
	if _, ok := w.(http.Flusher); !ok {
		err := sessionHTTPError{status: http.StatusInternalServerError, code: "streaming_not_supported", message: "response writer does not support streaming"}
		s.writeTurnError(w, err)
		return err
	}
	// A per-frame deadline must not leak onto the next keep-alive request.
	defer func() { _ = http.NewResponseController(w).SetWriteDeadline(time.Time{}) }()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	turn, early := startStreamTurn(ctx, session, prompt)
	if early != nil && early.Err != nil {
		s.writeTurnError(w, early.Err)
		return early.Err
	}
	streamHeaders(w)
	id := newID("chatcmpl")
	send := func(value any) error { return s.sendStreamFrame(w, "", value) }
	if err := send(streamChunk(id, model, choiceDelta{Role: "assistant"}, nil, nil)); err != nil {
		return err
	}
	completion, err := s.consumeText(ctx, turn, early, func(text string) error {
		return send(streamChunk(id, model, choiceDelta{Content: text}, nil, nil))
	})
	if err != nil {
		e := classifyError(err, "acp_turn_error")
		_ = send(openAIErrorResponse{Error: openAIError{Message: e.message, Type: e.code, Code: e.code}})
		return err
	}
	finish := finishReason(completion.StopReason)
	usage := usageFromACP(completion.Usage)
	if err := send(streamChunk(id, model, choiceDelta{}, &finish, usage)); err != nil {
		return err
	}
	if req.StreamOptions != nil && req.StreamOptions.IncludeUsage {
		if err := send(chatCompletionResponse{ID: id, Object: "chat.completion.chunk", Created: time.Now().Unix(), Model: model, Choices: []chatCompletionChoice{}, Usage: usage}); err != nil {
			return err
		}
	}
	return s.writeStreamFrame(w, "data: [DONE]\n\n")
}

func (s *Server) streamResponseTurn(ctx context.Context, w http.ResponseWriter, session *acp.Session, prompt string, req responseRequest, responseID string, model string) error {
	if _, ok := w.(http.Flusher); !ok {
		err := sessionHTTPError{status: http.StatusInternalServerError, code: "streaming_not_supported", message: "response writer does not support streaming"}
		s.writeTurnError(w, err)
		return err
	}
	// A per-frame deadline must not leak onto the next keep-alive request.
	defer func() { _ = http.NewResponseController(w).SetWriteDeadline(time.Time{}) }()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	turn, early := startStreamTurn(ctx, session, prompt)
	if early != nil && early.Err != nil {
		s.writeTurnError(w, early.Err)
		return early.Err
	}
	streamHeaders(w)
	itemID := newID("msg")
	created := time.Now().Unix()
	sequence := 0
	send := func(event string, value map[string]any) error {
		value["type"] = event
		value["sequence_number"] = sequence
		sequence++
		return s.sendStreamFrame(w, event, value)
	}
	if err := send("response.created", map[string]any{"response": responseSkeleton(responseID, model, created, "in_progress", req.Metadata)}); err != nil {
		return err
	}
	if err := send("response.output_item.added", map[string]any{"output_index": 0, "item": map[string]any{"id": itemID, "type": "message", "status": "in_progress", "role": "assistant", "content": []any{}}}); err != nil {
		return err
	}
	completion, err := s.consumeText(ctx, turn, early, func(text string) error {
		return send("response.output_text.delta", map[string]any{"item_id": itemID, "output_index": 0, "content_index": 0, "delta": text})
	})
	if err != nil {
		e := classifyError(err, "acp_turn_error")
		_ = send("response.failed", map[string]any{"error": openAIError{Message: e.message, Type: e.code, Code: e.code}, "response": responseSkeleton(responseID, model, created, "failed", req.Metadata)})
		return err
	}
	response := responseFromCompletion(responseID, model, completion, req.Metadata)
	response.CreatedAt = created
	response.Output[0].ID = itemID
	if err := send("response.output_text.done", map[string]any{"item_id": itemID, "output_index": 0, "content_index": 0, "text": completion.OutputText}); err != nil {
		return err
	}
	if err := send("response.completed", map[string]any{"response": response}); err != nil {
		return err
	}
	return s.writeStreamFrame(w, "data: [DONE]\n\n")
}
