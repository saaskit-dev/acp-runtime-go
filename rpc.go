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
	"unicode/utf8"
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
	detail := formatRPCErrorData(e.Data)
	if e.Code != 0 {
		if detail != "" {
			return fmt.Sprintf("rpc error %d: %s: %s", e.Code, e.Message, detail)
		}
		return fmt.Sprintf("rpc error %d: %s", e.Code, e.Message)
	}
	if detail != "" && e.Message != "" {
		return fmt.Sprintf("%s: %s", e.Message, detail)
	}
	return e.Message
}

func formatRPCErrorData(data json.RawMessage) string {
	if len(data) == 0 || string(data) == "null" {
		return ""
	}
	var text string
	if err := json.Unmarshal(data, &text); err == nil {
		return text
	}
	var fields map[string]any
	if err := json.Unmarshal(data, &fields); err == nil {
		for _, key := range []string{"message", "error", "detail", "reason", "stderr", "stdout"} {
			if value, ok := fields[key]; ok {
				if text, ok := value.(string); ok && text != "" {
					return text
				}
			}
		}
	}
	return string(data)
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
	maxRPCScanDepth          = 128
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
			if msg, ok := parseRPCMessage(line); ok {
				if len(msg.ID) > 0 && msg.Method == "" {
					p.resolvePending(msg)
				} else if msg.Method != "" {
					if len(msg.ID) > 0 {
						msg.ID = copyRawMessage(msg.ID)
						msg.Params = copyRawMessage(msg.Params)
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
	msg.Result = copyRawMessage(msg.Result)
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
		handler(ctx, copyRawMessage(msg.Params))
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
	if msg.JSONRPC == "2.0" && msg.Error == nil {
		if len(msg.ID) == 0 && msg.Method != "" && len(msg.Params) > 0 && len(msg.Result) == 0 {
			dst = append(dst, `{"jsonrpc":"2.0","method":`...)
			dst = appendMethodValue(dst, msg.Method)
			dst = append(dst, `,"params":`...)
			dst = append(dst, msg.Params...)
			dst = append(dst, '}')
			return dst
		}
		if len(msg.ID) > 0 && msg.Method != "" && len(msg.Params) > 0 && len(msg.Result) == 0 {
			dst = append(dst, `{"jsonrpc":"2.0","id":`...)
			dst = append(dst, msg.ID...)
			dst = append(dst, `,"method":`...)
			dst = appendMethodValue(dst, msg.Method)
			dst = append(dst, `,"params":`...)
			dst = append(dst, msg.Params...)
			dst = append(dst, '}')
			return dst
		}
		if len(msg.ID) > 0 && msg.Method == "" && len(msg.Result) > 0 {
			dst = append(dst, `{"jsonrpc":"2.0","id":`...)
			dst = append(dst, msg.ID...)
			dst = append(dst, `,"result":`...)
			dst = append(dst, msg.Result...)
			dst = append(dst, '}')
			return dst
		}
	}
	dst = append(dst, '{')
	first := true
	if msg.JSONRPC != "" {
		dst = appendStringField(dst, &first, "jsonrpc", msg.JSONRPC)
	}
	if len(msg.ID) > 0 {
		dst = appendRawField(dst, &first, "id", msg.ID)
	}
	if msg.Method != "" {
		dst = appendMethodField(dst, &first, msg.Method)
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

func appendMethodField(dst []byte, first *bool, method string) []byte {
	dst = appendFieldPrefix(dst, first, "method")
	return appendMethodValue(dst, method)
}

func appendMethodValue(dst []byte, method string) []byte {
	switch method {
	case "authenticate":
		return append(dst, `"authenticate"`...)
	case "fs/read_text_file":
		return append(dst, `"fs/read_text_file"`...)
	case "fs/write_text_file":
		return append(dst, `"fs/write_text_file"`...)
	case "initialize":
		return append(dst, `"initialize"`...)
	case "session/cancel":
		return append(dst, `"session/cancel"`...)
	case "session/close":
		return append(dst, `"session/close"`...)
	case "session/fork":
		return append(dst, `"session/fork"`...)
	case "session/list":
		return append(dst, `"session/list"`...)
	case "session/load":
		return append(dst, `"session/load"`...)
	case "session/new":
		return append(dst, `"session/new"`...)
	case "session/prompt":
		return append(dst, `"session/prompt"`...)
	case "session/request_permission":
		return append(dst, `"session/request_permission"`...)
	case "session/resume":
		return append(dst, `"session/resume"`...)
	case "session/set_config_option":
		return append(dst, `"session/set_config_option"`...)
	case "session/set_mode":
		return append(dst, `"session/set_mode"`...)
	case "session/update":
		return append(dst, `"session/update"`...)
	default:
		return strconv.AppendQuote(dst, method)
	}
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

func copyRawMessage(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	return append(json.RawMessage(nil), raw...)
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

func parseRPCMessage(line []byte) (rpcMessage, bool) {
	var msg rpcMessage
	line = trimJSONSpace(line)
	if len(line) < 2 || line[0] != '{' {
		return msg, false
	}
	i := 1
	for {
		i = skipJSONSpace(line, i)
		if i >= len(line) {
			return msg, false
		}
		if line[i] == '}' {
			return msg, skipJSONSpace(line, i+1) == len(line)
		}
		key, keyEscaped, next, ok := scanJSONString(line, i)
		if !ok {
			return msg, false
		}
		i = skipJSONSpace(line, next)
		if i >= len(line) || line[i] != ':' {
			return msg, false
		}
		i = skipJSONSpace(line, i+1)
		valueStart := i
		valueEnd, ok := scanJSONValue(line, i)
		if !ok {
			return msg, false
		}
		value := trimJSONSpace(line[valueStart:valueEnd])
		field := rpcFieldUnknown
		if !keyEscaped {
			field = matchRPCField(key)
		} else {
			keyString, ok := unquoteJSONString(key, true)
			if !ok {
				return msg, false
			}
			field = matchRPCFieldString(keyString)
		}
		switch field {
		case rpcFieldID:
			msg.ID = value
		case rpcFieldMethod:
			method, ok := parseRPCMethodValue(value)
			if !ok {
				return msg, false
			}
			msg.Method = method
		case rpcFieldParams:
			msg.Params = value
		case rpcFieldResult:
			msg.Result = value
		case rpcFieldError:
			var rpcErr RPCError
			if err := json.Unmarshal(value, &rpcErr); err != nil {
				return msg, false
			}
			msg.Error = &rpcErr
		case rpcFieldUnknown:
		}
		i = skipJSONSpace(line, valueEnd)
		if i >= len(line) {
			return msg, false
		}
		switch line[i] {
		case ',':
			i++
			if next := skipJSONSpace(line, i); next < len(line) && line[next] == '}' {
				return msg, false
			}
		case '}':
			return msg, skipJSONSpace(line, i+1) == len(line)
		default:
			return msg, false
		}
	}
}

type rpcField uint8

const (
	rpcFieldUnknown rpcField = iota
	rpcFieldID
	rpcFieldMethod
	rpcFieldParams
	rpcFieldResult
	rpcFieldError
)

func matchRPCField(key []byte) rpcField {
	switch len(key) {
	case 2:
		if equalASCII(key, "id") {
			return rpcFieldID
		}
	case 5:
		if equalASCII(key, "error") {
			return rpcFieldError
		}
	case 6:
		if equalASCII(key, "method") {
			return rpcFieldMethod
		}
		if equalASCII(key, "params") {
			return rpcFieldParams
		}
		if equalASCII(key, "result") {
			return rpcFieldResult
		}
	}
	return rpcFieldUnknown
}

func matchRPCFieldString(key string) rpcField {
	switch key {
	case "id":
		return rpcFieldID
	case "method":
		return rpcFieldMethod
	case "params":
		return rpcFieldParams
	case "result":
		return rpcFieldResult
	case "error":
		return rpcFieldError
	default:
		return rpcFieldUnknown
	}
}

func equalASCII(bytes []byte, value string) bool {
	if len(bytes) != len(value) {
		return false
	}
	for i := range bytes {
		if bytes[i] != value[i] {
			return false
		}
	}
	return true
}

func parseRPCMethodValue(value []byte) (string, bool) {
	raw, escaped, end, ok := scanJSONString(value, 0)
	if !ok || skipJSONSpace(value, end) != len(value) {
		return "", false
	}
	if !escaped {
		return canonicalRPCMethod(raw), true
	}
	return unquoteJSONString(raw, true)
}

func canonicalRPCMethod(raw []byte) string {
	if equalASCII(raw, "session/update") {
		return "session/update"
	}
	if equalASCII(raw, "session/prompt") {
		return "session/prompt"
	}
	if equalASCII(raw, "session/cancel") {
		return "session/cancel"
	}
	if equalASCII(raw, "initialize") {
		return "initialize"
	}
	if equalASCII(raw, "authenticate") {
		return "authenticate"
	}
	if equalASCII(raw, "session/new") {
		return "session/new"
	}
	if equalASCII(raw, "session/load") {
		return "session/load"
	}
	if equalASCII(raw, "session/resume") {
		return "session/resume"
	}
	if equalASCII(raw, "session/fork") {
		return "session/fork"
	}
	if equalASCII(raw, "session/list") {
		return "session/list"
	}
	if equalASCII(raw, "session/set_mode") {
		return "session/set_mode"
	}
	if equalASCII(raw, "session/set_config_option") {
		return "session/set_config_option"
	}
	if equalASCII(raw, "session/close") {
		return "session/close"
	}
	if equalASCII(raw, "session/request_permission") {
		return "session/request_permission"
	}
	if equalASCII(raw, "fs/read_text_file") {
		return "fs/read_text_file"
	}
	if equalASCII(raw, "fs/write_text_file") {
		return "fs/write_text_file"
	}
	return string(raw)
}

func scanJSONString(bytes []byte, i int) ([]byte, bool, int, bool) {
	if i >= len(bytes) || bytes[i] != '"' {
		return nil, false, i, false
	}
	start := i + 1
	escaped := false
	for i = start; i < len(bytes); i++ {
		switch bytes[i] {
		case '\\':
			escaped = true
			i++
			if i >= len(bytes) {
				return nil, escaped, i, false
			}
			switch bytes[i] {
			case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
			case 'u':
				if i+4 >= len(bytes) {
					return nil, escaped, i, false
				}
				for j := i + 1; j <= i+4; j++ {
					if !isJSONHex(bytes[j]) {
						return nil, escaped, j, false
					}
				}
				i += 4
			default:
				return nil, escaped, i, false
			}
		case '"':
			if !utf8.Valid(bytes[start:i]) {
				return nil, escaped, i, false
			}
			return bytes[start:i], escaped, i + 1, true
		case 0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15,
			16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31:
			return nil, escaped, i, false
		}
	}
	return nil, escaped, i, false
}

func isJSONHex(ch byte) bool {
	return (ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f') || (ch >= 'A' && ch <= 'F')
}

func unquoteJSONString(raw []byte, escaped bool) (string, bool) {
	if !escaped {
		return string(raw), true
	}
	quoted := make([]byte, 0, len(raw)+2)
	quoted = append(quoted, '"')
	quoted = append(quoted, raw...)
	quoted = append(quoted, '"')
	value, err := strconv.Unquote(string(quoted))
	return value, err == nil
}

func scanJSONValue(bytes []byte, i int) (int, bool) {
	return scanJSONValueDepth(bytes, i, 0)
}

func scanJSONValueDepth(bytes []byte, i int, depth int) (int, bool) {
	i = skipJSONSpace(bytes, i)
	if i >= len(bytes) {
		return i, false
	}
	switch bytes[i] {
	case '"':
		_, _, end, ok := scanJSONString(bytes, i)
		return end, ok
	case '{':
		return scanJSONObject(bytes, i, depth+1)
	case '[':
		return scanJSONArray(bytes, i, depth+1)
	default:
		return scanJSONLiteral(bytes, i)
	}
}

func scanJSONObject(bytes []byte, i int, depth int) (int, bool) {
	if depth > maxRPCScanDepth {
		return i, false
	}
	i++
	i = skipJSONSpace(bytes, i)
	if i < len(bytes) && bytes[i] == '}' {
		return i + 1, true
	}
	for {
		_, _, next, ok := scanJSONString(bytes, i)
		if !ok {
			return i, false
		}
		i = skipJSONSpace(bytes, next)
		if i >= len(bytes) || bytes[i] != ':' {
			return i, false
		}
		next, ok = scanJSONValueDepth(bytes, i+1, depth)
		if !ok {
			return i, false
		}
		i = skipJSONSpace(bytes, next)
		if i >= len(bytes) {
			return i, false
		}
		switch bytes[i] {
		case ',':
			i = skipJSONSpace(bytes, i+1)
		case '}':
			return i + 1, true
		default:
			return i, false
		}
	}
}

func scanJSONArray(bytes []byte, i int, depth int) (int, bool) {
	if depth > maxRPCScanDepth {
		return i, false
	}
	i++
	i = skipJSONSpace(bytes, i)
	if i < len(bytes) && bytes[i] == ']' {
		return i + 1, true
	}
	for {
		next, ok := scanJSONValueDepth(bytes, i, depth)
		if !ok {
			return i, false
		}
		i = skipJSONSpace(bytes, next)
		if i >= len(bytes) {
			return i, false
		}
		switch bytes[i] {
		case ',':
			i = skipJSONSpace(bytes, i+1)
		case ']':
			return i + 1, true
		default:
			return i, false
		}
	}
}

func scanJSONLiteral(bytes []byte, i int) (int, bool) {
	start := i
	for i < len(bytes) {
		switch bytes[i] {
		case ',', '}', ']', ' ', '\n', '\r', '\t':
			return i, isValidJSONLiteral(bytes[start:i])
		}
		i++
	}
	return i, isValidJSONLiteral(bytes[start:i])
}

func isValidJSONLiteral(value []byte) bool {
	if len(value) == 0 {
		return false
	}
	if equalASCII(value, "true") || equalASCII(value, "false") || equalASCII(value, "null") {
		return true
	}
	return isValidJSONNumber(value)
}

func isValidJSONNumber(value []byte) bool {
	i := 0
	if value[i] == '-' {
		i++
		if i == len(value) {
			return false
		}
	}
	if value[i] == '0' {
		i++
	} else if value[i] >= '1' && value[i] <= '9' {
		for i < len(value) && value[i] >= '0' && value[i] <= '9' {
			i++
		}
	} else {
		return false
	}
	if i < len(value) && value[i] == '.' {
		i++
		start := i
		for i < len(value) && value[i] >= '0' && value[i] <= '9' {
			i++
		}
		if i == start {
			return false
		}
	}
	if i < len(value) && (value[i] == 'e' || value[i] == 'E') {
		i++
		if i < len(value) && (value[i] == '+' || value[i] == '-') {
			i++
		}
		start := i
		for i < len(value) && value[i] >= '0' && value[i] <= '9' {
			i++
		}
		if i == start {
			return false
		}
	}
	return i == len(value)
}

func skipJSONSpace(bytes []byte, i int) int {
	for i < len(bytes) {
		switch bytes[i] {
		case ' ', '\n', '\r', '\t':
			i++
		default:
			return i
		}
	}
	return i
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
