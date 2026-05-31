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
	reader *bufio.Reader
	writer io.Writer
	opts   PeerOptions

	nextID  atomic.Int64
	writeMu sync.Mutex

	pendingMu sync.Mutex
	pending   map[int64]chan rpcMessage

	handlersMu           sync.RWMutex
	requestHandlers      map[string]RPCHandler
	notificationHandlers map[string]RPCNotificationHandler

	closed    chan struct{}
	closeOnce sync.Once
}

const (
	defaultRPCReadBufferSize = 64 * 1024
	maxRPCMessageSize        = 16 * 1024 * 1024
)

var (
	errRPCMessageTooLarge = errors.New("rpc message exceeds maximum size")
	rpcNullRawMessage     = json.RawMessage("null")
	rpcWriteBufferPool    = sync.Pool{New: func() any {
		buffer := make([]byte, 0, 512)
		return &buffer
	}}
)

func NewPeer(r io.Reader, w io.Writer, opts PeerOptions) *Peer {
	return &Peer{
		reader:               bufio.NewReaderSize(r, defaultRPCReadBufferSize),
		writer:               w,
		opts:                 opts,
		pending:              map[int64]chan rpcMessage{},
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
	for {
		line, err := p.readMessageLine()
		if len(line) > 0 {
			if p.opts.OnRawMessage != nil {
				p.opts.OnRawMessage("inbound", append(json.RawMessage(nil), line...))
			}
			var msg rpcMessage
			if unmarshalErr := json.Unmarshal(line, &msg); unmarshalErr == nil {
				if len(msg.ID) > 0 && msg.Method == "" {
					p.resolvePending(msg)
				} else if msg.Method != "" {
					if len(msg.ID) > 0 {
						go p.handleRequest(ctx, msg)
					} else {
						p.handleNotification(ctx, msg)
					}
				}
			}
		}
		if err != nil {
			p.Close()
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
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
	paramsBytes, err := marshalRaw(params)
	if err != nil {
		return err
	}
	response, err := p.callRaw(ctx, method, paramsBytes)
	if err != nil {
		return err
	}
	if result == nil {
		return nil
	}
	if len(response) == 0 || isRawNull(response) {
		return nil
	}
	return json.Unmarshal(response, result)
}

func (p *Peer) CallRaw(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
	return p.callRaw(ctx, method, normalizeRaw(params))
}

func (p *Peer) callRaw(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
	idValue := p.nextID.Add(1)
	var idBuffer [20]byte
	idBytes := strconv.AppendInt(idBuffer[:0], idValue, 10)
	msg := rpcMessage{JSONRPC: "2.0", ID: idBytes, Method: method, Params: params}
	ch := make(chan rpcMessage, 1)
	p.pendingMu.Lock()
	p.pending[idValue] = ch
	p.pendingMu.Unlock()
	if err := p.writeMessage(msg); err != nil {
		p.pendingMu.Lock()
		delete(p.pending, idValue)
		p.pendingMu.Unlock()
		return nil, err
	}
	select {
	case <-ctx.Done():
		p.pendingMu.Lock()
		delete(p.pending, idValue)
		p.pendingMu.Unlock()
		return nil, ctx.Err()
	case <-p.closed:
		return nil, io.ErrClosedPipe
	case response, ok := <-ch:
		if !ok {
			return nil, io.ErrClosedPipe
		}
		if response.Error != nil {
			return nil, response.Error
		}
		return response.Result, nil
	}
}

func (p *Peer) Notify(ctx context.Context, method string, params any) error {
	paramsBytes, err := marshalRaw(params)
	if err != nil {
		return err
	}
	return p.NotifyRaw(ctx, method, paramsBytes)
}

func (p *Peer) NotifyRaw(ctx context.Context, method string, params json.RawMessage) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return p.writeMessage(rpcMessage{JSONRPC: "2.0", Method: method, Params: normalizeRaw(params)})
	}
}

func (p *Peer) resolvePending(msg rpcMessage) {
	id, ok := parseRPCID(msg.ID)
	if !ok {
		return
	}
	p.pendingMu.Lock()
	ch := p.pending[id]
	delete(p.pending, id)
	p.pendingMu.Unlock()
	if ch != nil {
		ch <- msg
		close(ch)
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
	bufferPtr := rpcWriteBufferPool.Get().(*[]byte)
	buffer := appendRPCMessage((*bufferPtr)[:0], msg)

	p.writeMu.Lock()
	if p.opts.OnRawMessage != nil {
		p.opts.OnRawMessage("outbound", append(json.RawMessage(nil), buffer...))
	}
	buffer = append(buffer, '\n')
	_, err := p.writer.Write(buffer)
	p.writeMu.Unlock()
	recycleRPCWriteBuffer(bufferPtr, buffer)
	return err
}

func recycleRPCWriteBuffer(bufferPtr *[]byte, buffer []byte) {
	if cap(buffer) > maxRPCMessageSize {
		return
	}
	buffer = buffer[:0]
	*bufferPtr = buffer
	rpcWriteBufferPool.Put(bufferPtr)
}

func appendRPCMessage(dst []byte, msg rpcMessage) []byte {
	dst = append(dst, '{')
	first := true
	if msg.JSONRPC != "" {
		dst = appendStringField(dst, &first, "jsonrpc", msg.JSONRPC)
	}
	if len(msg.ID) > 0 {
		dst = appendRawField(dst, &first, "id", msg.ID)
	}
	if msg.Method != "" {
		dst = appendStringField(dst, &first, "method", msg.Method)
	}
	if len(msg.Params) > 0 {
		dst = appendRawField(dst, &first, "params", msg.Params)
	}
	if len(msg.Result) > 0 {
		dst = appendRawField(dst, &first, "result", msg.Result)
	}
	if msg.Error != nil {
		dst = appendFieldPrefix(dst, &first, "error")
		dst = appendRPCError(dst, msg.Error)
	}
	dst = append(dst, '}')
	return dst
}

func appendRPCError(dst []byte, rpcErr *RPCError) []byte {
	dst = append(dst, '{')
	first := true
	dst = appendFieldPrefix(dst, &first, "code")
	dst = strconv.AppendInt(dst, int64(rpcErr.Code), 10)
	dst = appendStringField(dst, &first, "message", rpcErr.Message)
	if len(rpcErr.Data) > 0 {
		dst = appendRawField(dst, &first, "data", rpcErr.Data)
	}
	dst = append(dst, '}')
	return dst
}

func appendStringField(dst []byte, first *bool, name string, value string) []byte {
	dst = appendFieldPrefix(dst, first, name)
	return strconv.AppendQuote(dst, value)
}

func appendRawField(dst []byte, first *bool, name string, value json.RawMessage) []byte {
	dst = appendFieldPrefix(dst, first, name)
	return append(dst, value...)
}

func appendFieldPrefix(dst []byte, first *bool, name string) []byte {
	if *first {
		*first = false
	} else {
		dst = append(dst, ',')
	}
	dst = append(dst, '"')
	dst = append(dst, name...)
	dst = append(dst, '"', ':')
	return dst
}

func marshalRaw(value any) (json.RawMessage, error) {
	if value == nil {
		return normalizeRaw(nil), nil
	}
	if raw, ok := value.(json.RawMessage); ok {
		return normalizeRaw(raw), nil
	}
	bytes, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(bytes), nil
}

func normalizeRaw(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return rpcNullRawMessage
	}
	return raw
}

func (p *Peer) readMessageLine() ([]byte, error) {
	var line []byte
	for {
		chunk, err := p.reader.ReadSlice('\n')
		if len(chunk) > 0 {
			if line == nil && err == nil {
				return trimLineEnding(chunk), nil
			}
			line = append(line, chunk...)
			if len(line) > maxRPCMessageSize {
				return nil, errRPCMessageTooLarge
			}
		}
		switch {
		case err == nil:
			return trimLineEnding(line), nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		default:
			if len(line) > 0 {
				return trimLineEnding(line), err
			}
			return nil, err
		}
	}
}

func trimLineEnding(line []byte) []byte {
	if len(line) > 0 && line[len(line)-1] == '\n' {
		line = line[:len(line)-1]
	}
	if len(line) > 0 && line[len(line)-1] == '\r' {
		line = line[:len(line)-1]
	}
	return line
}

func parseRPCID(raw json.RawMessage) (int64, bool) {
	id := trimJSONSpace(raw)
	if len(id) == 0 {
		return 0, false
	}
	if id[0] == '"' {
		if len(id) < 2 || id[len(id)-1] != '"' {
			return 0, false
		}
		return parsePositiveInt64Bytes(id[1 : len(id)-1])
	}
	return parsePositiveInt64Bytes(id)
}

func isRawNull(raw json.RawMessage) bool {
	raw = trimJSONSpace(raw)
	return len(raw) == 4 && raw[0] == 'n' && raw[1] == 'u' && raw[2] == 'l' && raw[3] == 'l'
}

func trimJSONSpace(bytes []byte) []byte {
	start := 0
	for start < len(bytes) {
		switch bytes[start] {
		case ' ', '\n', '\r', '\t':
			start++
		default:
			goto trimEnd
		}
	}
trimEnd:
	end := len(bytes)
	for end > start {
		switch bytes[end-1] {
		case ' ', '\n', '\r', '\t':
			end--
		default:
			return bytes[start:end]
		}
	}
	return bytes[start:end]
}

func parsePositiveInt64Bytes(bytes []byte) (int64, bool) {
	if len(bytes) == 0 {
		return 0, false
	}
	var value int64
	for _, b := range bytes {
		if b < '0' || b > '9' {
			return 0, false
		}
		digit := int64(b - '0')
		if value > (1<<63-1-digit)/10 {
			return 0, false
		}
		value = value*10 + digit
	}
	return value, true
}
