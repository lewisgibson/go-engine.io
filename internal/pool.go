// Package internal holds implementation utilities that are not part of the public
// go-engine.io API.
package internal

import "sync"

// Pool is a type-safe wrapper around sync.Pool. It pools values of type *T, so
// callers Get and Put a concrete pointer instead of asserting on any at every
// call site.
type Pool[T any] struct {
	pool sync.Pool
}

// NewPool creates a Pool whose Get calls newFn when the pool is empty.
func NewPool[T any](newFn func() *T) *Pool[T] {
	return &Pool[T]{
		pool: sync.Pool{
			New: func() any {
				return newFn()
			},
		},
	}
}

// Get returns a *T from the pool, creating one via newFn when the pool is empty.
func (p *Pool[T]) Get() *T {
	return p.pool.Get().(*T) //nolint:forcetypeassert,errcheck // *T is guaranteed by NewPool's constructor
}

// Put returns a *T to the pool for reuse. Reset it before putting it back.
func (p *Pool[T]) Put(x *T) {
	p.pool.Put(x)
}
