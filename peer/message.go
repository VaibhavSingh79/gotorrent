package peer

import (
	"encoding/binary"
	"fmt"
	"io"
)

type MessageID uint8

const (
	MsgChoke         MessageID = 0
	MsgUnchoke       MessageID = 1
	MsgInterested    MessageID = 2
	MsgNotInterested MessageID = 3
	MsgHave          MessageID = 4
	MsgBitfield      MessageID = 5
	MsgRequest       MessageID = 6
	MsgPiece         MessageID = 7
	MsgCancel        MessageID = 8
)

//a peer tells me how long the message is before it sends it, so a hostile one could
//claim 4GB and make me allocate it. 256KiB is way past the biggest real message (a 16KiB block)

const maxMessageSize = 1 << 18

//on the wire these are just numbers, I keep the names so my errors are readable

var messageNames = []string{"choke", "unchoke", "interested", "not interested",
	"have", "bitfield", "request", "piece", "cancel"}

func (id MessageID) String() string {
	if int(id) < len(messageNames) {
		return messageNames[id]
	}
	return fmt.Sprintf("unknown(%d)", uint8(id))
}

//every message is <4 byte length><1 byte id><payload>
//a nil *Message is the keep-alive, which is just a length of zero and nothing else

type Message struct {
	ID      MessageID
	Payload []byte
}

func (m *Message) Serialize() []byte {
	if m == nil {
		return make([]byte, 4) //keep-alive, four zero bytes
	}
	length := uint32(len(m.Payload) + 1) //+1 because the id byte counts toward the length
	buf := make([]byte, 4+length)
	binary.BigEndian.PutUint32(buf[0:4], length)
	buf[4] = byte(m.ID)
	copy(buf[5:], m.Payload)
	return buf
}

//returns (nil, nil) for a keep-alive, so callers have to check for a nil message

func ReadMessage(r io.Reader) (*Message, error) {
	lengthBuf := make([]byte, 4)
	if _, err := io.ReadFull(r, lengthBuf); err != nil {
		return nil, err
	}

	length := binary.BigEndian.Uint32(lengthBuf)
	if length == 0 {
		return nil, nil
	}
	if length > maxMessageSize {
		return nil, fmt.Errorf("peer sent an oversized message of %d bytes", length)
	}

	buf := make([]byte, length)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, err
	}

	return &Message{ID: MessageID(buf[0]), Payload: buf[1:]}, nil
}

//I never ask for a whole piece at once, I ask for 16KiB blocks inside it
//so the request is which piece, how far into it, and how many bytes

func FormatRequest(index, begin, length int) *Message {
	payload := make([]byte, 12)
	binary.BigEndian.PutUint32(payload[0:4], uint32(index))
	binary.BigEndian.PutUint32(payload[4:8], uint32(begin))
	binary.BigEndian.PutUint32(payload[8:12], uint32(length))
	return &Message{ID: MsgRequest, Payload: payload}
}

func FormatHave(index int) *Message {
	payload := make([]byte, 4)
	binary.BigEndian.PutUint32(payload, uint32(index))
	return &Message{ID: MsgHave, Payload: payload}
}

func ParseHave(msg *Message) (int, error) {
	if msg.ID != MsgHave {
		return 0, fmt.Errorf("expected have, got %s", msg.ID)
	}
	if len(msg.Payload) != 4 {
		return 0, fmt.Errorf("have payload is %d bytes, want 4", len(msg.Payload))
	}
	return int(binary.BigEndian.Uint32(msg.Payload)), nil
}

//copies the block into buf and tells me how many bytes landed
//everything here is a bounds check because the peer picks these numbers, not me

func ParsePiece(index int, buf []byte, msg *Message) (int, error) {
	if msg.ID != MsgPiece {
		return 0, fmt.Errorf("expected piece, got %s", msg.ID)
	}
	if len(msg.Payload) < 8 {
		return 0, fmt.Errorf("piece payload is %d bytes, want at least 8", len(msg.Payload))
	}

	parsedIndex := int(binary.BigEndian.Uint32(msg.Payload[0:4]))
	if parsedIndex != index {
		return 0, fmt.Errorf("expected piece %d, got piece %d", index, parsedIndex)
	}

	begin := int(binary.BigEndian.Uint32(msg.Payload[4:8]))
	if begin >= len(buf) {
		return 0, fmt.Errorf("block offset %d is past the end of piece %d (%d bytes)", begin, index, len(buf))
	}

	data := msg.Payload[8:]
	if begin+len(data) > len(buf) {
		return 0, fmt.Errorf("block of %d bytes at offset %d overruns piece %d (%d bytes)", len(data), begin, index, len(buf))
	}

	copy(buf[begin:], data)
	return len(data), nil
}
