package engineio_test

import (
	"errors"
	"net/http"
	"net/url"
	"testing"

	engineio "github.com/lewisgibson/go-engine.io"
	"github.com/stretchr/testify/require"
)

func TestNewPollingTransport(t *testing.T) {
	t.Parallel()

	// Arrange: parse the target url
	u, err := url.Parse("http://localhost/engine.io/?EIO=4&transport=polling")
	require.NoError(t, err)

	// Act: create a new polling transport
	transport, err := engineio.NewPollingTransport(u, http.DefaultClient, http.Header{})
	require.NoError(t, err)

	// Assert: the transport is a polling transport with the correct state
	require.Equal(t, engineio.TransportTypePolling, transport.Type())
	require.Equal(t, engineio.TransportStateClosed, transport.State())
}

func TestNewPollingTransport_NilURL(t *testing.T) {
	t.Parallel()

	// Act: create a new polling transport
	transport, err := engineio.NewPollingTransport(nil, http.DefaultClient, http.Header{})

	// Assert: the ErrURLRequired error is returned
	require.ErrorIs(t, err, engineio.ErrURLRequired)
	require.Nil(t, transport)
}

// badReadWriteCloser is a mock io.ReadWriteCloser that returns an error when read or write is called.
type badReadWriteCloser struct{}

// Read implements the io.Reader interface.
func (b *badReadWriteCloser) Read(p []byte) (n int, err error) {
	return 0, errors.New("mock error")
}

// Write implements the io.Writer interface.
func (b *badReadWriteCloser) Write(p []byte) (n int, err error) {
	return 0, errors.New("mock error")
}

// Close implements the io.Closer interface.
func (b *badReadWriteCloser) Close() error {
	return nil
}
