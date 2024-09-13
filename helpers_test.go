package engineio_test

import (
	"testing"

	engineio "github.com/lewisgibson/go-engine.io"
	"github.com/stretchr/testify/require"
)

func TestPointer(t *testing.T) {
	t.Parallel()

	// Arrange: create a value
	value := 42

	// Act: get a pointer to the value
	pointer := engineio.Pointer(value)

	// Assert: the pointer is the address of the value
	require.Equal(t, &value, pointer)
}
