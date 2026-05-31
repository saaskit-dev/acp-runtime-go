package acpruntime

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"sync"
	"sync/atomic"
)

type RPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *RPCError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Code != 0 {
		return fmt.Sprintf("rpc error %d: %s", e.Code, e.Message)
	}
	return e.Message
}

type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc,omitempty"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

type RPCHandler func(context.Context, json.RawMessage) (any, error)
type RPCNotificationHandler func(context.Context, json.RawMessage)

type PeerOptions struct {
	OnRawMessage func(direction string, message json.RawMessage)
}

type Peer struct {
	reader *bufio.Scanner
	writer io.Writer
	opts   PeerOptions

	nextID  atomic.Int64
	writeMu sync.Mutex

	pendingMu sync.Mutex
	pending   map[string]chan rpcMessage

	handlersMu           sync.RWMutex
	requestHandlers      map[string]RPCHandler
	notificationHandlers map[string]RPCNotificationHandler

	closed    chan struct{}
	closeOnce sync.Once
}

func NewPeer(r io.Reader, w io.Writer, opts PeerOptions) *Peer {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	return &Peer{
		reader:               scanner,
		writer:               w,
		opts:                 opts,
		pending:              map[string]chan rpcMessage{},
		requestHandlers:      map[string]RPCHandler{},
		notificationHandlers: map[string]RPCNotificationHandler{},
		closed:               make(chan struct{}),
	}
}

func (p *Peer) RegisterRequest(method string, handler RPCHandler) {
	p.handlersMu.Lock()
	defer p.handlersMu.Unlock()
	p.requestHandlers[method] = handler
}

func (p *Peer) RegisterNotification(method string, handler RPCNotificationHandler) {
	p.handlersMu.Lock()
	defer p.handlersMu.Unlock()
	p.notificationHandlers[method] = handler
}

func (p *Peer) Start(ctx context.Context) error {
	for p.reader.Scan() {
		line := append([]byte(nil), p.reader.Bytes()...)
		if len(line) == 0 {
			continue
		}
		if p.opts.OnRawMessage != nil {
			p.opts.OnRawMessage("inbound", line)
		}
		var msg rpcMessage
		if err := json.Unmarshal(line, &msg); err != nil {
			continue
		}
		if len(msg.ID) > 0 && msg.Method == "" {
			p.resolvePending(msg)
			continue
		}
		if msg.Method != "" {
			if len(msg.ID) > 0 {
				go p.handleRequest(ctx, msg)
			} else {
				p.handleNotification(ctx, msg)
			}
		}
	}
	p.Close()
	if err := p.reader.Err(); err != nil {
		return err
	}
	return nil
}

func (p *Peer) Done() <-chan struct{} {
	return p.closed
}

func (p *Peer) Close() {
	p.closeOnce.Do(func() {
		close(p.closed)
		p.pendingMu.Lock()
		for id, ch := range p.pending {
			delete(p.pending, id)
			close(ch)
		}
		p.pendingMu.Unlock()
	})
}

func (p *Peer) Call(ctx context.Context, method string, params any, result any) error {
	idValue := p.nextID.Add(1)
	id := strconv.FormatInt(idValue, 10)
	paramsBytes, err := marshalRaw(params)
	if err != nil {
		return err
	}
	idBytes, _ := json.Marshal(idValue)
	msg := rpcMessage{JSONRPC: "2.0", ID: idBytes, Method: method, Params: paramsBytes}
	ch := make(chan rpcMessage, 1)
	p.pendingMu.Lock()
	p.pending[id] = ch
	p.pendingMu.Unlock()
	if err := p.writeMessage(msg); err != nil {
		p.pendingMu.Lock()
		delete(p.pending, id)
		p.pendingMu.Unlock()
		return err
	}
	select {
	case <-ctx.Done():
		p.pendingMu.Lock()
		delete(p.pending, id)
		p.pendingMu.Unlock()
		return ctx.Err()
	case <-p.closed:
		return io.ErrClosedPipe
	case response, ok := <-ch:
		if !ok {
			return io.ErrClosedPipe
		}
		if response.Error != nil {
			return response.Error
		}
		if result == nil {
			return nil
		}
		if len(response.Result) == 0 || string(response.Result) == "null" {
			return nil
		}
		return json.Unmarshal(response.Result, result)
	}
}

func (p *Peer) Notify(ctx context.Context, method string, params any) error {
	paramsBytes, err := marshalRaw(params)
	if err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return p.writeMessage(rpcMessage{JSONRPC: "2.0", Method: method, Params: paramsBytes})
	}
}

func (p *Peer) resolvePending(msg rpcMessage) {
	var idNumber int64
	if err := json.Unmarshal(msg.ID, &idNumber); err == nil {
		p.pendingMu.Lock()
		ch := p.pending[strconv.FormatInt(idNumber, 10)]
		delete(p.pending, strconv.FormatInt(idNumber, 10))
		p.pendingMu.Unlock()
		if ch != nil {
			ch <- msg
			close(ch)
		}
		return
	}
	var idString string
	if err := json.Unmarshal(msg.ID, &idString); err == nil {
		p.pendingMu.Lock()
		ch := p.pending[idString]
		delete(p.pending, idString)
		p.pendingMu.Unlock()
		if ch != nil {
			ch <- msg
			close(ch)
		}
	}
}

func (p *Peer) handleRequest(ctx context.Context, msg rpcMessage) {
	p.handlersMu.RLock()
	handler := p.requestHandlers[msg.Method]
	p.handlersMu.RUnlock()
	if handler == nil {
		_ = p.writeMessage(rpcMessage{JSONRPC: "2.0", ID: msg.ID, Error: &RPCError{Code: -32601, Message: "method not found"}})
		return
	}
	result, err := handler(ctx, msg.Params)
	if err != nil {
		var rpcErr *RPCError
		if !errors.As(err, &rpcErr) {
			rpcErr = &RPCError{Code: -32000, Message: err.Error()}
		}
		_ = p.writeMessage(rpcMessage{JSONRPC: "2.0", ID: msg.ID, Error: rpcErr})
		return
	}
	resultBytes, err := marshalRaw(result)
	if err != nil {
		_ = p.writeMessage(rpcMessage{JSONRPC: "2.0", ID: msg.ID, Error: &RPCError{Code: -32603, Message: err.Error()}})
		return
	}
	_ = p.writeMessage(rpcMessage{JSONRPC: "2.0", ID: msg.ID, Result: resultBytes})
}

func (p *Peer) handleNotification(ctx context.Context, msg rpcMessage) {
	p.handlersMu.RLock()
	handler := p.notificationHandlers[msg.Method]
	p.handlersMu.RUnlock()
	if handler != nil {
		handler(ctx, msg.Params)
	}
}

func (p *Peer) writeMessage(msg rpcMessage) error {
	bytes, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	if p.opts.OnRawMessage != nil {
		p.opts.OnRawMessage("outbound", bytes)
	}
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	_, err = p.writer.Write(append(bytes, '\n'))
	return err
}

func marshalRaw(value any) (json.RawMessage, error) {
	if value == nil {
		return json.RawMessage("null"), nil
	}
	if raw, ok := value.(json.RawMessage); ok {
		if len(raw) == 0 {
			return json.RawMessage("null"), nil
		}
		return raw, nil
	}
	bytes, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(bytes), nil
}
