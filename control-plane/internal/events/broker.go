// Package events provides a per-user pub/sub broker used to stream tunnel
// lifecycle changes to connected clients (SSE) instead of client polling.
package events

import (
	"sync"
)

// Broker fans messages out to every subscriber of a given user. A subscriber
// is a connected SSE stream. Subscribers are added/removed per connection, so
// publishing stays O(subscribers) and needs no server-side polling.
type Broker struct {
	mu   sync.RWMutex
	subs map[string]map[chan []byte]struct{}
}

// New creates an empty broker.
func New() *Broker {
	return &Broker{subs: make(map[string]map[chan []byte]struct{})}
}

// Subscribe registers a channel for user and returns it plus an unsubscribe
// func. The channel is buffered so a slow client drops messages rather than
// blocking a tunnel operation.
func (b *Broker) Subscribe(userID string) (<-chan []byte, func()) {
	ch := make(chan []byte, 64)
	b.mu.Lock()
	if b.subs[userID] == nil {
		b.subs[userID] = make(map[chan []byte]struct{})
	}
	b.subs[userID][ch] = struct{}{}
	b.mu.Unlock()

	once := sync.Once{}
	cancel := func() {
		once.Do(func() {
			b.mu.Lock()
			if set := b.subs[userID]; set != nil {
				delete(set, ch)
				if len(set) == 0 {
					delete(b.subs, userID)
				}
			}
			b.mu.Unlock()
			close(ch)
		})
	}
	return ch, cancel
}

// HasSubscribers reports whether anyone is currently listening for user.
// The stats broadcaster uses this to skip marshalling payloads nobody will
// receive -- with the control plane on a home server and clients scattered
// across the internet, most users are offline most of the time.
func (b *Broker) HasSubscribers(userID string) bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.subs[userID]) > 0
}

// Publish delivers a raw payload to every subscriber of user. Non-blocking:
// slow or closed subscribers are dropped.
func (b *Broker) Publish(userID string, payload []byte) {
	b.mu.RLock()
	set := b.subs[userID]
	for ch := range set {
		select {
		case ch <- payload:
		default:
		}
	}
	b.mu.RUnlock()
}

// SubscriberCount reports how many live SSE streams a user has (used by tests
// and metrics).
func (b *Broker) SubscriberCount(userID string) int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.subs[userID])
}
