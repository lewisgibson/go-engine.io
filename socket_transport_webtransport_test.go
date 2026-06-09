package engineio_test

import (
	"net/url"
	"testing"

	engineio "github.com/lewisgibson/go-engine.io"
	"github.com/quic-go/webtransport-go"
	"github.com/stretchr/testify/require"
)

func TestNewWebTransportTransport(t *testing.T) {
	t.Parallel()

	// Arrange: parse the target url.
	u, err := url.Parse("https://localhost/engine.io/?EIO=4&transport=webtransport")
	require.NoError(t, err)

	// Act: create a new webtransport transport.
	transport, err := engineio.NewWebTransportTransport(u, &webtransport.Dialer{}, nil)
	require.NoError(t, err)

	// Assert: the transport is a webtransport transport in the closed state.
	require.Equal(t, engineio.TransportTypeWebTransport, transport.Type())
	require.Equal(t, engineio.TransportStateClosed, transport.State())
}

func TestNewWebTransportTransport_NilURL(t *testing.T) {
	t.Parallel()

	// Act: create a transport with no url.
	transport, err := engineio.NewWebTransportTransport(nil, &webtransport.Dialer{}, nil)

	// Assert: the ErrURLRequired error is returned and no transport is built.
	require.ErrorIs(t, err, engineio.ErrURLRequired)
	require.Nil(t, transport)
}

func TestNewWebTransportTransport_NilDialer(t *testing.T) {
	t.Parallel()

	// Arrange: parse the target url.
	u, err := url.Parse("https://localhost/engine.io/")
	require.NoError(t, err)

	// Act: create a transport with no dialer.
	transport, err := engineio.NewWebTransportTransport(u, nil, nil)

	// Assert: the ErrWebTransportDialerRequired error is returned and no transport
	// is built.
	require.ErrorIs(t, err, engineio.ErrWebTransportDialerRequired)
	require.Nil(t, transport)
}
