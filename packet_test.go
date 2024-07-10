package engineio_test

import (
	"testing"

	engineio "github.com/lewisgibson/go-engine.io"
	"github.com/stretchr/testify/assert"
)

func TestPacketType_String(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "open", engineio.PacketOpen.String())
	assert.Equal(t, "close", engineio.PacketClose.String())
	assert.Equal(t, "ping", engineio.PacketPing.String())
	assert.Equal(t, "pong", engineio.PacketPong.String())
	assert.Equal(t, "message", engineio.PacketMessage.String())
	assert.Equal(t, "upgrade", engineio.PacketUpgrade.String())
	assert.Equal(t, "noop", engineio.PacketNoop.String())
	assert.Equal(t, "unknown", engineio.PacketType(100).String())
}

func TestPacketType_Byte(t *testing.T) {
	t.Parallel()

	assert.Equal(t, byte('0'), engineio.PacketOpen.Byte())
	assert.Equal(t, byte('1'), engineio.PacketClose.Byte())
	assert.Equal(t, byte('2'), engineio.PacketPing.Byte())
	assert.Equal(t, byte('3'), engineio.PacketPong.Byte())
	assert.Equal(t, byte('4'), engineio.PacketMessage.Byte())
	assert.Equal(t, byte('5'), engineio.PacketUpgrade.Byte())
	assert.Equal(t, byte('6'), engineio.PacketNoop.Byte())
}

func TestPacketTypeFromByte(t *testing.T) {
	t.Parallel()

	assert.Equal(t, engineio.PacketOpen, engineio.PacketTypeFromByte(byte('0')))
	assert.Equal(t, engineio.PacketClose, engineio.PacketTypeFromByte(byte('1')))
	assert.Equal(t, engineio.PacketPing, engineio.PacketTypeFromByte(byte('2')))
	assert.Equal(t, engineio.PacketPong, engineio.PacketTypeFromByte(byte('3')))
	assert.Equal(t, engineio.PacketMessage, engineio.PacketTypeFromByte(byte('4')))
	assert.Equal(t, engineio.PacketUpgrade, engineio.PacketTypeFromByte(byte('5')))
	assert.Equal(t, engineio.PacketNoop, engineio.PacketTypeFromByte(byte('6')))
}

func TestPacketTypeFromInt(t *testing.T) {
	t.Parallel()

	assert.Equal(t, engineio.PacketOpen, engineio.PacketTypeFromInt(0))
	assert.Equal(t, engineio.PacketClose, engineio.PacketTypeFromInt(1))
	assert.Equal(t, engineio.PacketPing, engineio.PacketTypeFromInt(2))
	assert.Equal(t, engineio.PacketPong, engineio.PacketTypeFromInt(3))
	assert.Equal(t, engineio.PacketMessage, engineio.PacketTypeFromInt(4))
	assert.Equal(t, engineio.PacketUpgrade, engineio.PacketTypeFromInt(5))
	assert.Equal(t, engineio.PacketNoop, engineio.PacketTypeFromInt(6))
}
