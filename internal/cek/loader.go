package cek

import (
	"fmt"
	"sync"

	"regel.dev/regel/internal/rast"
)

// DefSource loads a definition's canonical AST by its ADR-02 content address.
// In integration it is backed by the catalog; in unit tests by a map.
type DefSource interface {
	Load(hash string) (*rast.Node, error)
}

// MapSource is an in-memory DefSource for unit tests.
type MapSource map[string]*rast.Node

func (m MapSource) Load(hash string) (*rast.Node, error) {
	n, ok := m[hash]
	if !ok {
		return nil, fmt.Errorf("cek: definition %s not found", hash)
	}
	return n, nil
}

// Interp holds the immutable, process-wide machinery: the definition source, an
// immortal in-process AST cache (definitions are content-addressed and immortal,
// so caching is unconditionally safe — ADR-03 I6), and the native dispatch
// registry (ADR-04 §5).
type Interp struct {
	src DefSource
	reg *Registry

	mu    sync.RWMutex
	cache map[string]*rast.Node
}

// New builds an interpreter over a DefSource and a native Registry.
func New(src DefSource, reg *Registry) *Interp {
	if reg == nil {
		reg = NewRegistry()
	}
	return &Interp{src: src, reg: reg, cache: map[string]*rast.Node{}}
}

// loadAST returns the cached AST for a definition hash, loading it once.
func (in *Interp) loadAST(hash string) (*rast.Node, error) {
	in.mu.RLock()
	n, ok := in.cache[hash]
	in.mu.RUnlock()
	if ok {
		return n, nil
	}
	n, err := in.src.Load(hash)
	if err != nil {
		return nil, err
	}
	in.mu.Lock()
	in.cache[hash] = n
	in.mu.Unlock()
	return n, nil
}
