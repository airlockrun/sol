// Package bus provides a typed event bus for inter-component communication.
// Based on opencode's bus system.
package bus

import (
	"sync"
	"sync/atomic"
)

// Event represents a published event with type and properties.
type Event struct {
	Type       string
	Properties any
}

// Subscription is a callback that receives events.
type Subscription func(Event)

// subscription wraps a callback with an ID for removal.
type subscription struct {
	id       uint64
	callback Subscription
}

// Bus is the main event bus for publishing and subscribing to events.
type Bus struct {
	mu            sync.RWMutex
	subscriptions map[string][]subscription
	nextID        atomic.Uint64
}

// New creates a new Bus instance.
func New() *Bus {
	return &Bus{
		subscriptions: make(map[string][]subscription),
	}
}

// Publish sends an event to all subscribers of that event type and wildcard subscribers.
func (b *Bus) Publish(eventType string, properties any) {
	b.mu.RLock()
	// Copy slices to avoid holding lock during callbacks
	var toCall []Subscription
	if subs, ok := b.subscriptions[eventType]; ok {
		for _, sub := range subs {
			toCall = append(toCall, sub.callback)
		}
	}
	if subs, ok := b.subscriptions["*"]; ok {
		for _, sub := range subs {
			toCall = append(toCall, sub.callback)
		}
	}
	b.mu.RUnlock()

	event := Event{
		Type:       eventType,
		Properties: properties,
	}

	for _, cb := range toCall {
		cb(event)
	}
}

// Subscribe registers a callback for a specific event type.
// Returns an unsubscribe function.
func (b *Bus) Subscribe(eventType string, callback Subscription) func() {
	id := b.nextID.Add(1)

	b.mu.Lock()
	b.subscriptions[eventType] = append(b.subscriptions[eventType], subscription{
		id:       id,
		callback: callback,
	})
	b.mu.Unlock()

	return func() {
		b.mu.Lock()
		defer b.mu.Unlock()

		subs := b.subscriptions[eventType]
		for i, sub := range subs {
			if sub.id == id {
				b.subscriptions[eventType] = append(subs[:i], subs[i+1:]...)
				break
			}
		}
	}
}

// SubscribeAll registers a callback for all events (wildcard).
func (b *Bus) SubscribeAll(callback Subscription) func() {
	return b.Subscribe("*", callback)
}

// Once subscribes to an event and automatically unsubscribes after the callback returns true.
func (b *Bus) Once(eventType string, callback func(Event) bool) {
	var unsub func()
	var once sync.Once
	unsub = b.Subscribe(eventType, func(e Event) {
		if callback(e) {
			once.Do(func() {
				if unsub != nil {
					unsub()
				}
			})
		}
	})
}

