package engineio

import (
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"slices"
	"time"
)

// Default server options.
const (
	// DefaultPingInterval is how often the server sends a ping.
	DefaultPingInterval = 25 * time.Second
	// DefaultPingTimeout is how long the server waits for a pong before closing.
	DefaultPingTimeout = 20 * time.Second
	// DefaultUpgradeTimeout is how long a transport upgrade probe may take.
	DefaultUpgradeTimeout = 10 * time.Second
	// DefaultMaxPayload is the maximum accepted POST body size, in bytes. It also
	// bounds a single inbound WebTransport frame.
	DefaultMaxPayload = 1_000_000
)

// CORSOptions configures the cross-origin headers the server sets. An empty
// AllowedOrigins allows every origin.
type CORSOptions struct {
	// AllowCredentials sets Access-Control-Allow-Credentials. When true the
	// server echoes the request origin rather than replying with "*".
	AllowCredentials bool
	// AllowedOrigins is the set of origins permitted to connect. An empty slice
	// allows every origin. A single "*" entry also allows every origin.
	AllowedOrigins []string
	// AllowedHeaders is the set of request headers advertised in the preflight
	// response. An empty slice advertises Content-Type.
	AllowedHeaders []string
}

// serverConfig holds the resolved server options.
type serverConfig struct {
	pingInterval    time.Duration
	pingTimeout     time.Duration
	upgradeTimeout  time.Duration
	maxPayload      int
	transports      []TransportType
	allowUpgrades   bool
	cors            CORSOptions
	generateID      func(r *http.Request) string
	allowRequest    func(r *http.Request) error
	cookie          *CookieOptions
	httpCompression bool
	// webTransportUpgrade serves a WebTransport request. It is nil unless
	// WithWebTransportServer is set; storing it as a closure keeps the webtransport-go
	// import out of this file and server.go.
	webTransportUpgrade func(s *Server, w http.ResponseWriter, r *http.Request)
}

// CookieOptions configures the session-affinity cookie set on the handshake
// response. It is used for sticky sessions behind a load balancer that routes by
// cookie, so a client's long-polling and upgrade requests reach the same node.
type CookieOptions struct {
	// Name is the cookie name. Default "io".
	Name string
	// Path is the cookie path. Default "/".
	Path string
	// HTTPOnly sets the HttpOnly attribute. Recommended true.
	HTTPOnly bool
	// Secure sets the Secure attribute (cookie sent over HTTPS only).
	Secure bool
	// SameSite sets the SameSite attribute. Default http.SameSiteLaxMode.
	SameSite http.SameSite
	// MaxAge sets Max-Age in seconds. Zero leaves it unset (a session cookie).
	MaxAge int
}

// ServerOption configures a Server.
type ServerOption func(*serverConfig)

// WithPingInterval sets how often the server sends a ping. Default: 25s.
func WithPingInterval(pingInterval time.Duration) ServerOption {
	return func(c *serverConfig) {
		c.pingInterval = pingInterval
	}
}

// WithPingTimeout sets how long the server waits for a pong before closing the
// session with reason "ping timeout". Default: 20s.
func WithPingTimeout(pingTimeout time.Duration) ServerOption {
	return func(c *serverConfig) {
		c.pingTimeout = pingTimeout
	}
}

// WithUpgradeTimeout bounds how long a transport upgrade probe may take before
// it is abandoned. Default: 10s.
func WithUpgradeTimeout(upgradeTimeout time.Duration) ServerOption {
	return func(c *serverConfig) {
		c.upgradeTimeout = upgradeTimeout
	}
}

// WithMaxPayload sets the maximum accepted POST body size in bytes; a larger body
// is rejected with HTTP 413. The same limit bounds a single inbound WebTransport
// frame, which is rejected as a parse error (not an HTTP status) when it exceeds
// it; a non-positive value still bounds a WebTransport frame at an internal 16 MiB
// ceiling, so a read can never be left unbounded. Default: 1_000_000.
func WithMaxPayload(maxPayload int) ServerOption {
	return func(c *serverConfig) {
		c.maxPayload = maxPayload
	}
}

// WithServerTransports sets the transports the server accepts.
// Default: polling and websocket.
func WithServerTransports(transports ...TransportType) ServerOption {
	return func(c *serverConfig) {
		c.transports = transports
	}
}

// WithAllowUpgrades determines whether the server advertises and accepts
// transport upgrades. Default: true.
func WithAllowUpgrades(allowUpgrades bool) ServerOption {
	return func(c *serverConfig) {
		c.allowUpgrades = allowUpgrades
	}
}

// WithCORS configures the cross-origin headers the server sets.
func WithCORS(cors CORSOptions) ServerOption {
	return func(c *serverConfig) {
		c.cors = cors
	}
}

// WithGenerateID sets the function that returns a new session identifier for a
// handshake. It receives the handshake request, so the identifier can be derived
// from request data such as an authenticated user resolved from a header or
// token. The returned identifier must be unique across live sessions; returning
// a duplicate of an existing session's identifier is undefined.
// Default: 18 random bytes, base64url-encoded.
func WithGenerateID(generateID func(r *http.Request) string) ServerOption {
	return func(c *serverConfig) {
		c.generateID = generateID
	}
}

// WithAllowRequest sets a gate run for every handshake before a session is
// allocated. Returning a non-nil error rejects the handshake with HTTP 403 and
// the Engine.IO "Forbidden" error, and the error's message is reported to the
// connection-error handler. It is the place for authentication, token checks,
// and rate limiting. Default: unset (every handshake is allowed).
func WithAllowRequest(allowRequest func(r *http.Request) error) ServerOption {
	return func(c *serverConfig) {
		c.allowRequest = allowRequest
	}
}

// WithCookie enables a session-affinity cookie on the handshake response for
// sticky sessions behind a cookie-routing load balancer. Empty Name, Path, and
// SameSite fields default to "io", "/", and Lax. Default: no cookie.
func WithCookie(cookie CookieOptions) ServerOption {
	if cookie.Name == "" {
		cookie.Name = "io"
	}
	if cookie.Path == "" {
		cookie.Path = "/"
	}
	if cookie.SameSite == http.SameSiteDefaultMode {
		cookie.SameSite = http.SameSiteLaxMode
	}

	return func(c *serverConfig) {
		c.cookie = &cookie
	}
}

// WithHTTPCompression enables or disables gzip compression of long-poll response
// bodies whose size is at least an internal threshold, when the client advertises
// gzip in its Accept-Encoding header. Default: true, matching the reference server.
func WithHTTPCompression(httpCompression bool) ServerOption {
	return func(c *serverConfig) {
		c.httpCompression = httpCompression
	}
}

// defaultServerConfig returns the server configuration with every default
// applied.
func defaultServerConfig() serverConfig {
	return serverConfig{
		pingInterval:    DefaultPingInterval,
		pingTimeout:     DefaultPingTimeout,
		upgradeTimeout:  DefaultUpgradeTimeout,
		maxPayload:      DefaultMaxPayload,
		transports:      []TransportType{TransportTypePolling, TransportTypeWebSocket},
		allowUpgrades:   true,
		generateID:      generateSessionID,
		httpCompression: true,
	}
}

// allowsTransport reports whether the named transport is enabled.
func (c serverConfig) allowsTransport(transport TransportType) bool {
	return slices.Contains(c.transports, transport)
}

// generateSessionID returns a new session identifier built from 18 random
// bytes, base64url-encoded without padding. It ignores the request; it is the
// default for WithGenerateID, whose signature it must match.
func generateSessionID(*http.Request) string {
	var buffer [18]byte
	if _, err := rand.Read(buffer[:]); err != nil {
		// crypto/rand.Read never returns an error on supported platforms; panic
		// rather than hand out a predictable identifier.
		panic("engineio: unable to read random bytes for session id: " + err.Error())
	}

	return base64.RawURLEncoding.EncodeToString(buffer[:])
}
