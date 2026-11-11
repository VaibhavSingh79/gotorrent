package peer

import (
	"bytes"
	"net"
	"testing"
)

func TestBitfield(t *testing.T) {
	//0b01010100, 0b01010100 -> pieces 1,3,5,9,11,13 set
	bf := Bitfield{0b01010100, 0b01010100}
	want := map[int]bool{
		0: false, 1: true, 2: false, 3: true, 4: false, 5: true, 6: false, 7: false,
		8: false, 9: true, 10: false, 11: true, 12: false, 13: true,
		19: false, // out of range
		-1: false,
	}
	for index, expected := range want {
		if got := bf.HasPiece(index); got != expected {
			t.Errorf("HasPiece(%d) = %v, want %v", index, got, expected)
		}
	}

	bf2 := make(Bitfield, 2)
	bf2.SetPiece(0)
	bf2.SetPiece(9)
	bf2.SetPiece(100) // out of range, must not panic
	if !bf2.HasPiece(0) || !bf2.HasPiece(9) {
		t.Errorf("SetPiece did not set the expected bits: %08b", bf2)
	}
	if bf2.HasPiece(1) {
		t.Error("SetPiece set a bit it should not have")
	}
}

func TestUnmarshalPeers(t *testing.T) {
	bin := []byte{
		127, 0, 0, 1, 0x1A, 0xE1, // 127.0.0.1:6881
		192, 168, 1, 5, 0x1A, 0xE9, // 192.168.1.5:6889
	}
	peers, err := Unmarshal(bin)
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if len(peers) != 2 {
		t.Fatalf("got %d peers, want 2", len(peers))
	}
	if got := peers[0].String(); got != "127.0.0.1:6881" {
		t.Errorf("peers[0] = %s, want 127.0.0.1:6881", got)
	}
	if got := peers[1].String(); got != "192.168.1.5:6889" {
		t.Errorf("peers[1] = %s, want 192.168.1.5:6889", got)
	}

	if _, err := Unmarshal([]byte{1, 2, 3}); err == nil {
		t.Error("expected an error for a malformed peer list")
	}
}

func TestHandshakeRoundTrip(t *testing.T) {
	infoHash := [20]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20}
	peerID := [20]byte{20, 19, 18, 17, 16, 15, 14, 13, 12, 11, 10, 9, 8, 7, 6, 5, 4, 3, 2, 1}

	h := NewHandshake(infoHash, peerID)
	wire := h.Serialize()
	if len(wire) != 68 {
		t.Fatalf("handshake is %d bytes, want 68", len(wire))
	}
	if wire[0] != 19 || string(wire[1:20]) != "BitTorrent protocol" {
		t.Errorf("bad protocol header: %q", wire[:20])
	}
	if !bytes.Equal(wire[20:28], make([]byte, 8)) {
		t.Errorf("reserved bytes are not zero: %v", wire[20:28])
	}

	got, err := ReadHandshake(bytes.NewReader(wire))
	if err != nil {
		t.Fatalf("ReadHandshake: %v", err)
	}
	if got.InfoHash != infoHash {
		t.Errorf("info hash = %x, want %x", got.InfoHash, infoHash)
	}
	if got.PeerID != peerID {
		t.Errorf("peer id = %x, want %x", got.PeerID, peerID)
	}
}

func TestMessageRoundTrip(t *testing.T) {
	msg := &Message{ID: MsgPiece, Payload: []byte{1, 2, 3, 4}}
	got, err := ReadMessage(bytes.NewReader(msg.Serialize()))
	if err != nil {
		t.Fatalf("ReadMessage: %v", err)
	}
	if got.ID != MsgPiece || !bytes.Equal(got.Payload, msg.Payload) {
		t.Errorf("round trip = %v %v, want %v %v", got.ID, got.Payload, msg.ID, msg.Payload)
	}

	//A nil message is a keep-alive: four zero bytes, and reads back as nil.
	var keepAlive *Message
	if wire := keepAlive.Serialize(); !bytes.Equal(wire, []byte{0, 0, 0, 0}) {
		t.Errorf("keep-alive = %v, want four zero bytes", wire)
	}
	got, err = ReadMessage(bytes.NewReader([]byte{0, 0, 0, 0}))
	if err != nil || got != nil {
		t.Errorf("keep-alive read = %v, %v; want nil, nil", got, err)
	}
}

func TestFormatAndParseRequest(t *testing.T) {
	msg := FormatRequest(4, 567, 4321)
	if msg.ID != MsgRequest {
		t.Errorf("ID = %v, want request", msg.ID)
	}
	want := []byte{0, 0, 0, 4, 0, 0, 2, 55, 0, 0, 16, 225}
	if !bytes.Equal(msg.Payload, want) {
		t.Errorf("payload = %v, want %v", msg.Payload, want)
	}
}

func TestParseHave(t *testing.T) {
	index, err := ParseHave(FormatHave(4))
	if err != nil || index != 4 {
		t.Errorf("ParseHave = %d, %v; want 4, nil", index, err)
	}
	if _, err := ParseHave(&Message{ID: MsgHave, Payload: []byte{1, 2}}); err == nil {
		t.Error("expected an error for a short have payload")
	}
	if _, err := ParseHave(&Message{ID: MsgPiece}); err == nil {
		t.Error("expected an error for the wrong message type")
	}
}

func TestParsePiece(t *testing.T) {
	buf := make([]byte, 10)
	msg := &Message{ID: MsgPiece, Payload: []byte{
		0, 0, 0, 4, // index 4
		0, 0, 0, 2, // begin 2
		0xDE, 0xAD, 0xBE, 0xEF,
	}}
	n, err := ParsePiece(4, buf, msg)
	if err != nil {
		t.Fatalf("ParsePiece: %v", err)
	}
	if n != 4 {
		t.Errorf("wrote %d bytes, want 4", n)
	}
	if !bytes.Equal(buf, []byte{0, 0, 0xDE, 0xAD, 0xBE, 0xEF, 0, 0, 0, 0}) {
		t.Errorf("buf = %v", buf)
	}

	//Each of these must be rejected rather than panicking or corrupting buf.
	bad := []struct {
		name string
		msg  *Message
	}{
		{"wrong index", &Message{ID: MsgPiece, Payload: []byte{0, 0, 0, 5, 0, 0, 0, 0, 1}}},
		{"wrong type", &Message{ID: MsgHave, Payload: []byte{0, 0, 0, 4, 0, 0, 0, 0, 1}}},
		{"short payload", &Message{ID: MsgPiece, Payload: []byte{0, 0, 0, 4}}},
		{"offset past end", &Message{ID: MsgPiece, Payload: []byte{0, 0, 0, 4, 0, 0, 0, 20, 1}}},
		{"block overruns", &Message{ID: MsgPiece, Payload: []byte{0, 0, 0, 4, 0, 0, 0, 8, 1, 2, 3, 4, 5}}},
	}
	for _, tc := range bad {
		if _, err := ParsePiece(4, make([]byte, 10), tc.msg); err == nil {
			t.Errorf("%s: expected an error", tc.name)
		}
	}
}

func TestPeerString(t *testing.T) {
	p := Peer{IP: net.ParseIP("10.0.0.1"), Port: 1234}
	if got := p.String(); got != "10.0.0.1:1234" {
		t.Errorf("String() = %s, want 10.0.0.1:1234", got)
	}
}
