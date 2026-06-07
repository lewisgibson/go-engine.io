package engineio

import "sync"

// sessionStore is a concurrent map of session identifiers to their sockets. Its
// mutex guards only the map; it is never held while doing I/O or while a session
// lock is held.
type sessionStore struct {
	mu       sync.RWMutex
	sessions map[string]*ServerSocket
}

// newSessionStore creates an empty session store.
func newSessionStore() *sessionStore {
	return &sessionStore{sessions: make(map[string]*ServerSocket)}
}

// get returns the socket for the given session identifier.
func (s *sessionStore) get(id string) (*ServerSocket, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if socket, ok := s.sessions[id]; ok {
		return socket, true
	}
	return nil, false
}

// put stores a socket under its session identifier.
func (s *sessionStore) put(socket *ServerSocket) {
	s.mu.Lock()
	s.sessions[socket.id] = socket
	s.mu.Unlock()
}

// delete removes a socket by its session identifier.
func (s *sessionStore) delete(id string) {
	s.mu.Lock()
	delete(s.sessions, id)
	s.mu.Unlock()
}

// all returns a snapshot of every stored socket.
func (s *sessionStore) all() []*ServerSocket {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var sockets = make([]*ServerSocket, 0, len(s.sessions))
	for _, socket := range s.sessions {
		sockets = append(sockets, socket)
	}

	return sockets
}

// count returns the number of stored sockets.
func (s *sessionStore) count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.sessions)
}
