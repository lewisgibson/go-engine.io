package engineio

import (
	"encoding/json"
	"fmt"
	"time"
)

// buildOpenPacket constructs the handshake open packet for a new session. When
// offerUpgrades is true and upgrades are enabled, it advertises the websocket
// upgrade.
func (s *Server) buildOpenPacket(id string, offerUpgrades bool) (Packet, error) {
	var upgrades = []TransportType{}
	if offerUpgrades && s.options.allowUpgrades && s.options.allowsTransport(TransportTypeWebSocket) {
		upgrades = []TransportType{TransportTypeWebSocket}
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
