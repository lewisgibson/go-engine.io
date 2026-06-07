package internal_test

import (
	"sync"
	"testing"

	"github.com/lewisgibson/go-engine.io/internal"
	"github.com/stretchr/testify/require"
)

func TestPool_GetCreatesWhenEmpty(t *testing.T) {
	t.Parallel()

	// Arrange: a pool whose constructor returns a known value
	pool := internal.NewPool(func() *int { return new(42) })

	// Act: get from the empty pool
	value := pool.Get()

	// Assert: the constructor supplied a fresh value
	require.NotNil(t, value)
	require.Equal(t, 42, *value)
}

func TestPool_GetAfterPutReturnsUsableValue(t *testing.T) {
	t.Parallel()

	// Arrange: a pool with one value taken and mutated
	pool := internal.NewPool(func() *int { return new(0) })
	value := pool.Get()
	*value = 99

	// Act: return it and get again
	pool.Put(value)
	reused := pool.Get()

	// Assert: a usable value comes back. sync.Pool may reuse the put value or drop
	// it; it never resets, so the same backing still reads 99 while a fresh one is
	// the constructor's zero.
	require.NotNil(t, reused)
	if reused == value {
		require.Equal(t, 99, *reused)
	} else {
		require.Equal(t, 0, *reused)
	}
}

func TestPool_ConcurrentGetPutIsRaceFree(t *testing.T) {
	t.Parallel()

	// Arrange: a pool shared across many goroutines
	pool := internal.NewPool(func() *int { return new(0) })

	const goroutines = 50
	const operations = 200

	// Act: hammer Get/Put concurrently
	var group sync.WaitGroup
	for range goroutines {
		group.Go(func() {
			for index := range operations {
				value := pool.Get()
				*value = index
				pool.Put(value)
			}
		})
	}
	group.Wait()

	// Assert: a value is still usable after the contention
	require.NotNil(t, pool.Get())
}
