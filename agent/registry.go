package agent

import (
	"fmt"
	"sync"
)

// Factory creates an Agent with tools appropriate for the given model ID.
type Factory func(modelID string) *Agent

// Registry manages available agent factories.
type Registry struct {
	mu        sync.RWMutex
	factories map[string]Factory
}

// NewRegistry creates a new agent registry.
func NewRegistry() *Registry {
	return &Registry{
		factories: make(map[string]Factory),
	}
}

// Register adds a factory to the registry.
func (r *Registry) Register(name string, factory Factory) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if name == "" {
		return fmt.Errorf("agent name cannot be empty")
	}

	if _, exists := r.factories[name]; exists {
		return fmt.Errorf("agent %q already registered", name)
	}

	r.factories[name] = factory
	return nil
}

// MustRegister adds a factory to the registry, panicking on error.
func (r *Registry) MustRegister(name string, factory Factory) {
	if err := r.Register(name, factory); err != nil {
		panic(err)
	}
}

// Get retrieves an agent by name, building it with the given model ID.
func (r *Registry) Get(name, modelID string) (*Agent, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	factory, exists := r.factories[name]
	if !exists {
		return nil, false
	}
	return factory(modelID), true
}

// MustGet retrieves an agent by name, panicking if not found.
func (r *Registry) MustGet(name, modelID string) *Agent {
	agent, exists := r.Get(name, modelID)
	if !exists {
		panic(fmt.Sprintf("agent %q not found", name))
	}
	return agent
}

// GetFactory retrieves the factory function for an agent by name.
func (r *Registry) GetFactory(name string) (Factory, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	factory, exists := r.factories[name]
	return factory, exists
}

// List returns all registered agent names.
func (r *Registry) List() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	names := make([]string, 0, len(r.factories))
	for name := range r.factories {
		names = append(names, name)
	}
	return names
}

// DefaultRegistry is the global agent registry.
var DefaultRegistry = NewRegistry()

// Register adds a factory to the default registry.
func Register(name string, factory Factory) error {
	return DefaultRegistry.Register(name, factory)
}

// MustRegister adds a factory to the default registry, panicking on error.
func MustRegister(name string, factory Factory) {
	DefaultRegistry.MustRegister(name, factory)
}

// Get retrieves an agent from the default registry.
func Get(name, modelID string) (*Agent, bool) {
	return DefaultRegistry.Get(name, modelID)
}

// MustGet retrieves an agent from the default registry, panicking if not found.
func MustGet(name, modelID string) *Agent {
	return DefaultRegistry.MustGet(name, modelID)
}

// GetFactory retrieves a factory from the default registry.
func GetFactory(name string) (Factory, bool) {
	return DefaultRegistry.GetFactory(name)
}

// List returns all agent names from the default registry.
func List() []string {
	return DefaultRegistry.List()
}
