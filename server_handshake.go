package engineio

import (
	"encoding/json"
	"fmt"
	"time"
)

// buildOpenPacket constructs the handshake open packet for a new session. When
// offerUpgrades is true and upgrades are enabled, it advertises every configured
// upgrade transport in order: websocket, and webtransport when WithWebTransportServer
// wired an HTTP/3 upgrader to serve it.
func (s *Server) buildOpenPacket(id string, offerUpgrades bool) (Packet, error) {
	var upgrades = []TransportType{}
	if offerUpgrades && s.options.allowUpgrades {
		for _, transport := range s.options.transports {
			switch transport {
			case TransportTypeWebSocket:
				upgrades = append(upgrades, transport)

			// WebTransport is only a real upgrade target when an HTTP/3 upgrader is
			// wired; advertising it otherwise would send clients probing a transport
			// the server cannot serve.
			case TransportTypeWebTransport:
				if s.options.webTransportUpgrade != nil {
					upgrades = append(upgrades, transport)
				}
			}
		}
	}

	data, err := json.Marshal(OpenPacket{
		SessionID:    id,
		Upgrades:     upgrades,
		PingInterval: int(s.options.pingInterval / time.Millisecond),
		PingTimeout:  int(s.options.pingTimeout / time.Millisecond),
		MaxPayload:   s.options.maxPayload,
	})
	if err != nil {
		return Packet{}, fmt.Errorf("marshalling open packet: %w", err)
	}

	return Packet{Type: PacketOpen, Data: data}, nil
}
