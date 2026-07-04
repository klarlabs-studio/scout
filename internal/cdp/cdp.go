// Package cdp provides a low-level Chrome DevTools Protocol client over WebSocket.
package cdp

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

// Message represents a CDP JSON-RPC message.
type Message struct {
	ID        int64           `json:"id,omitempty"`
	SessionID string          `json:"sessionId,omitempty"`
	Method    string          `json:"method,omitempty"`
	Params    json.RawMessage `json:"params,omitempty"`
	Result    json.RawMessage `json:"result,omitempty"`
	Error     *RPCError       `json:"error,omitempty"`
}

// RPCError represents a CDP error response.
type RPCError struct {
	Code    int64  `json:"code"`
	Message string `json:"message"`
}

func (e *RPCError) Error() string {
	return fmt.Sprintf("cdp: error %d: %s", e.Code, e.Message)
}

// eventKey uniquely identifies a session-scoped event handler.
type eventKey struct {
	sessionID string
	method    string
}

// handlerEntry holds a handler and its cancellation flag.
type handlerEntry struct {
	fn        func(json.RawMessage)
	cancelled atomic.Bool
}

// queuedEvent is a received CDP event awaiting dispatch off the read loop.
type queuedEvent struct {
	sessionID string
	method    string
	params    json.RawMessage
}

// Conn is a WebSocket connection to a CDP target.
type Conn struct {
	ws        *websocket.Conn
	nextID    atomic.Int64
	pending   map[int64]chan *Message
	pendingMu sync.Mutex
	wsMu      sync.Mutex // separate lock for WebSocket writes
	events    map[eventKey][]*handlerEntry
	eventsMu  sync.RWMutex
	closed    chan struct{}
	isClosed  atomic.Bool
	stopPing  chan struct{} // signals the keepalive goroutine to stop

	// Event handlers run on a dedicated dispatch goroutine fed by this unbounded
	// queue, so a handler that makes a CDP call never blocks readLoop — which
	// must stay free to route that call's response (otherwise the two deadlock).
	eventQueue []queuedEvent
	eventMu    sync.Mutex
	eventCond  *sync.Cond
}

// DefaultCallTimeout is the maximum time to wait for a CDP response.
var DefaultCallTimeout = 30 * time.Second

const (
	// pingInterval is how often we send WebSocket pings to keep the connection alive.
	pingInterval = 30 * time.Second
	// pingTimeout is how long we wait for a pong before considering the connection dead.
	pingTimeout = 60 * time.Second
)

// Dial connects to a CDP WebSocket endpoint.
func Dial(url string) (*Conn, error) {
	dialer := websocket.Dialer{
		ReadBufferSize:  1 << 20, // 1MB for large responses (screenshots)
		WriteBufferSize: 32 * 1024,
	}
	ws, _, err := dialer.Dial(url, nil)
	if err != nil {
		return nil, fmt.Errorf("cdp: failed to connect to %s: %w", url, err)
	}
	ws.SetReadLimit(64 * 1024 * 1024) // 64MB max message size

	c := &Conn{
		ws:       ws,
		pending:  make(map[int64]chan *Message),
		events:   make(map[eventKey][]*handlerEntry),
		closed:   make(chan struct{}),
		stopPing: make(chan struct{}),
	}
	c.eventCond = sync.NewCond(&c.eventMu)

	// Set pong handler to extend the read deadline on each pong received, and
	// seed an initial deadline so a peer that goes silent before the first pong
	// still trips ReadMessage instead of blocking readLoop forever.
	ws.SetPongHandler(func(string) error {
		return ws.SetReadDeadline(time.Now().Add(pingTimeout))
	})
	_ = ws.SetReadDeadline(time.Now().Add(pingTimeout))

	go c.readLoop()
	go c.dispatchLoop()
	go c.pingLoop()
	return c, nil
}

// Call sends a CDP method call (browser-level, no session).
func (c *Conn) Call(method string, params any) (json.RawMessage, error) {
	return c.CallSession("", method, params)
}

// CallSession sends a CDP method call on a specific session.
// Uses DefaultCallTimeout. For context-aware calls, use CallSessionCtx.
func (c *Conn) CallSession(sessionID, method string, params any) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(context.Background(), DefaultCallTimeout)
	defer cancel()
	return c.CallSessionCtx(ctx, sessionID, method, params)
}

// CallSessionCtx sends a CDP method call with context-based cancellation.
func (c *Conn) CallSessionCtx(ctx context.Context, sessionID, method string, params any) (json.RawMessage, error) {
	id := c.nextID.Add(1)

	var rawParams json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return nil, err
		}
		rawParams = b
	}

	msg := Message{
		ID:        id,
		SessionID: sessionID,
		Method:    method,
		Params:    rawParams,
	}

	ch := make(chan *Message, 1)
	c.pendingMu.Lock()
	c.pending[id] = ch
	c.pendingMu.Unlock()

	data, err := json.Marshal(msg)
	if err != nil {
		return nil, err
	}

	c.wsMu.Lock()
	err = c.ws.WriteMessage(websocket.TextMessage, data)
	c.wsMu.Unlock()
	if err != nil {
		c.pendingMu.Lock()
		delete(c.pending, id)
		c.pendingMu.Unlock()
		return nil, fmt.Errorf("cdp: write error: %w", err)
	}

	select {
	case resp, ok := <-ch:
		if !ok {
			return nil, fmt.Errorf("cdp: connection closed while waiting for response")
		}
		if resp.Error != nil {
			return nil, resp.Error
		}
		return resp.Result, nil
	case <-c.closed:
		return nil, fmt.Errorf("cdp: connection closed")
	case <-ctx.Done():
		c.pendingMu.Lock()
		delete(c.pending, id)
		c.pendingMu.Unlock()
		return nil, fmt.Errorf("cdp: %w waiting for %s (id=%d)", ctx.Err(), method, id)
	}
}

// On registers a global event handler (no session filtering).
// Returns an unsubscribe function.
func (c *Conn) On(method string, handler func(json.RawMessage)) func() {
	return c.OnSession("", method, handler)
}

// OnSession registers an event handler scoped to a specific session.
// Events from other sessions are ignored. Returns an unsubscribe function.
func (c *Conn) OnSession(sessionID, method string, handler func(json.RawMessage)) func() {
	entry := &handlerEntry{fn: handler}
	key := eventKey{sessionID: sessionID, method: method}

	c.eventsMu.Lock()
	c.events[key] = append(c.events[key], entry)
	c.eventsMu.Unlock()

	return func() {
		// Flag first so an in-flight dispatch that already snapshotted this entry
		// skips it, then remove it so it stops being iterated and can be GC'd —
		// otherwise re-subscribing observers (network, screencast) accumulate dead
		// entries for the life of the connection.
		entry.cancelled.Store(true)
		c.eventsMu.Lock()
		entries := c.events[key]
		for i, e := range entries {
			if e == entry {
				c.events[key] = append(entries[:i], entries[i+1:]...)
				break
			}
		}
		if len(c.events[key]) == 0 {
			delete(c.events, key)
		}
		c.eventsMu.Unlock()
	}
}

// RemoveSessionHandlers removes all event handlers for a given session.
func (c *Conn) RemoveSessionHandlers(sessionID string) {
	c.eventsMu.Lock()
	for key := range c.events {
		if key.sessionID == sessionID {
			delete(c.events, key)
		}
	}
	c.eventsMu.Unlock()
}

// Close closes the WebSocket connection.
func (c *Conn) Close() error {
	if !c.isClosed.CompareAndSwap(false, true) {
		return nil
	}
	close(c.closed)
	close(c.stopPing)
	c.wakeDispatch()
	return c.ws.Close()
}

// pingLoop sends periodic WebSocket pings to keep the connection alive.
// Chrome's CDP WebSocket can drop idle connections; this prevents that.
func (c *Conn) pingLoop() {
	ticker := time.NewTicker(pingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			c.wsMu.Lock()
			err := c.ws.WriteControl(
				websocket.PingMessage,
				nil,
				time.Now().Add(10*time.Second),
			)
			c.wsMu.Unlock()
			if err != nil {
				return // connection is dead, readLoop will handle cleanup
			}
		case <-c.stopPing:
			return
		case <-c.closed:
			return
		}
	}
}

func (c *Conn) readLoop() {
	for {
		_, data, err := c.ws.ReadMessage()
		if err != nil {
			c.pendingMu.Lock()
			for _, ch := range c.pending {
				close(ch)
			}
			c.pending = make(map[int64]chan *Message)
			c.pendingMu.Unlock()

			if c.isClosed.CompareAndSwap(false, true) {
				close(c.closed)
			}
			c.wakeDispatch()
			return
		}

		var msg Message
		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}

		if msg.ID > 0 {
			c.pendingMu.Lock()
			ch, ok := c.pending[msg.ID]
			if ok {
				delete(c.pending, msg.ID)
			}
			c.pendingMu.Unlock()
			if ok {
				ch <- &msg
			}
		} else if msg.Method != "" {
			c.enqueueEvent(msg.SessionID, msg.Method, msg.Params)
		}
	}
}

// enqueueEvent appends an event to the dispatch queue and wakes the dispatch
// goroutine. It never blocks the read loop.
func (c *Conn) enqueueEvent(sessionID, method string, params json.RawMessage) {
	c.eventMu.Lock()
	c.eventQueue = append(c.eventQueue, queuedEvent{sessionID: sessionID, method: method, params: params})
	c.eventCond.Signal()
	c.eventMu.Unlock()
}

// wakeDispatch wakes the dispatch goroutine so it can observe a closed
// connection and exit once it has drained any remaining events.
func (c *Conn) wakeDispatch() {
	c.eventMu.Lock()
	c.eventCond.Broadcast()
	c.eventMu.Unlock()
}

// dispatchLoop delivers queued events to their handlers on a single goroutine,
// preserving event order while keeping readLoop free to route call responses.
// Because it runs off the read loop, a handler may itself make a CDP call
// without deadlocking. It drains remaining events after Close, then exits.
func (c *Conn) dispatchLoop() {
	for {
		c.eventMu.Lock()
		for len(c.eventQueue) == 0 && !c.isClosed.Load() {
			c.eventCond.Wait()
		}
		if len(c.eventQueue) == 0 { // closed and drained
			c.eventMu.Unlock()
			return
		}
		ev := c.eventQueue[0]
		c.eventQueue = c.eventQueue[1:]
		if len(c.eventQueue) == 0 {
			c.eventQueue = nil // release the backing array once drained
		}
		c.eventMu.Unlock()

		c.dispatchEvent(ev.sessionID, ev.method, ev.params)
	}
}

func (c *Conn) dispatchEvent(sessionID, method string, params json.RawMessage) {
	c.eventsMu.RLock()
	// Collect handlers: session-scoped first, then global
	var handlers []*handlerEntry
	if sessionID != "" {
		handlers = append(handlers, c.events[eventKey{sessionID: sessionID, method: method}]...)
	}
	handlers = append(handlers, c.events[eventKey{sessionID: "", method: method}]...)
	c.eventsMu.RUnlock()

	for _, h := range handlers {
		if h.cancelled.Load() {
			continue
		}
		c.invokeHandler(h, params)
	}
}

// invokeHandler runs one handler, recovering from a panic so a single bad
// handler can't take down the dispatch goroutine — and with it every future
// event and call-response for the connection.
func (c *Conn) invokeHandler(h *handlerEntry, params json.RawMessage) {
	defer func() { _ = recover() }()
	h.fn(params)
}
