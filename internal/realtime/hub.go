// Package realtime is the in-process fan-out used by the notification stream.
// It exists only to push "something changed" signals to already authenticated
// users: it never stores data and never decides visibility.
package realtime

import (
	"errors"
	"sync"
)

const (
	// DefaultPerUser bounds how many streams one account may keep open. A normal
	// user has one tab; five leaves room for a few windows without letting a
	// client exhaust the process.
	DefaultPerUser = 5
	// DefaultMaxSubscribers bounds the streams of the whole installation. It is
	// deliberately far above normal use: the limit exists to fail predictably.
	DefaultMaxSubscribers = 500
	// subscriberBuffer is the number of frames a slow client may lag behind
	// before it is asked to resync and disconnected.
	subscriberBuffer = 16
)

var (
	ErrTooManyStreams = errors.New("too many notification streams")
	ErrClosed         = errors.New("notification hub closed")
)

// Event is one named Server-Sent Event frame. Data must already be JSON.
type Event struct {
	Name string
	Data []byte
}

// Subscriber is one open stream. Events is read by the HTTP handler; Done is
// closed when the hub drops the subscriber because it is too slow or because
// the process is shutting down.
type Subscriber struct {
	hub      *Hub
	userID   int64
	events   chan Event
	done     chan struct{}
	closeOne sync.Once
}

// Events returns the buffered frames waiting for the client.
func (s *Subscriber) Events() <-chan Event { return s.events }

// Done is closed when the subscriber must stop. The handler writes a final
// resync frame and returns.
func (s *Subscriber) Done() <-chan struct{} { return s.done }

// Close removes the subscriber from the hub. It is idempotent and safe to call
// from a deferred handler cleanup after the hub already dropped it.
func (s *Subscriber) Close() { s.hub.unsubscribe(s) }

func (s *Subscriber) closeSignal() {
	s.closeOne.Do(func() { close(s.done) })
}

// Hub keeps subscribers grouped by user. There is one hub per process, which
// matches the single-instance SQLite deployment. Publishing is non-blocking: a
// subscriber whose buffer is full is removed and asked to resync.
type Hub struct {
	mu       sync.Mutex
	subs     map[int64]map[*Subscriber]struct{}
	total    int
	perUser  int
	maxTotal int
	closed   bool
}

func NewHub() *Hub {
	return &Hub{
		subs:     make(map[int64]map[*Subscriber]struct{}),
		perUser:  DefaultPerUser,
		maxTotal: DefaultMaxSubscribers,
	}
}

// Subscribe registers one stream for userID. The caller owns the subscriber
// and must call Close when the request ends.
func (h *Hub) Subscribe(userID int64) (*Subscriber, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil, ErrClosed
	}
	if len(h.subs[userID]) >= h.perUser || h.total >= h.maxTotal {
		return nil, ErrTooManyStreams
	}
	sub := &Subscriber{
		hub:    h,
		userID: userID,
		events: make(chan Event, subscriberBuffer),
		done:   make(chan struct{}),
	}
	if h.subs[userID] == nil {
		h.subs[userID] = make(map[*Subscriber]struct{})
	}
	h.subs[userID][sub] = struct{}{}
	h.total++
	return sub, nil
}

// Publish sends ev to every stream of userID. A full buffer means the client is
// not reading fast enough: the subscriber is removed and signalled so the
// handler can send resync and let EventSource reconnect.
func (h *Hub) Publish(userID int64, ev Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	for sub := range h.subs[userID] {
		select {
		case sub.events <- ev:
		default:
			h.removeLocked(sub)
			sub.closeSignal()
		}
	}
}

func (h *Hub) unsubscribe(sub *Subscriber) {
	h.mu.Lock()
	h.removeLocked(sub)
	h.mu.Unlock()
	sub.closeSignal()
}

func (h *Hub) removeLocked(sub *Subscriber) {
	set, ok := h.subs[sub.userID]
	if !ok {
		return
	}
	if _, ok := set[sub]; !ok {
		return
	}
	delete(set, sub)
	if len(set) == 0 {
		delete(h.subs, sub.userID)
	}
	h.total--
}

// Close drops every subscriber and refuses new ones. It is idempotent and is
// called before http.Server.Shutdown so open streams cannot block the drain.
func (h *Hub) Close() {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}
	h.closed = true
	subs := make([]*Subscriber, 0, h.total)
	for _, set := range h.subs {
		for sub := range set {
			subs = append(subs, sub)
		}
	}
	h.subs = make(map[int64]map[*Subscriber]struct{})
	h.total = 0
	h.mu.Unlock()
	for _, sub := range subs {
		sub.closeSignal()
	}
}

// Counters reports the number of accounts with an open stream and the total
// number of streams. It exists for tests and operational assertions.
func (h *Hub) Counters() (users, total int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs), h.total
}
