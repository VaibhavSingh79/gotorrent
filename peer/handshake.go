package peer

import (
	"fmt"
	"io"
)

const protocolID = "BitTorrent protocol"

//the first thing both sides send, always 68 bytes:
//<1 byte length=19><"BitTorrent protocol"><8 reserved zeros><20 byte info hash><20 byte peer id>

type Handshake struct {
	Pstr     string
	InfoHash [20]byte
	PeerID   [20]byte
}

func NewHandshake(infoHash, peerID [20]byte) *Handshake {
	return &Handshake{Pstr: protocolID, InfoHash: infoHash, PeerID: peerID}
}

func (h *Handshake) Serialize() []byte {
	buf := make([]byte, len(h.Pstr)+49) //49 = 1 length byte + 8 reserved + 20 + 20
	buf[0] = byte(len(h.Pstr))
	n := 1
	n += copy(buf[n:], h.Pstr)
	n += copy(buf[n:], make([]byte, 8)) //reserved, I don't use any extensions so all zero
	n += copy(buf[n:], h.InfoHash[:])
	copy(buf[n:], h.PeerID[:])
	return buf
}

func ReadHandshake(r io.Reader) (*Handshake, error) {
	//the length is the first byte, and I need it before I know how much more to read
	lengthBuf := make([]byte, 1)
	if _, err := io.ReadFull(r, lengthBuf); err != nil {
		return nil, err
	}

	pstrlen := int(lengthBuf[0])
	if pstrlen == 0 {
		return nil, fmt.Errorf("handshake has a zero-length protocol identifier")
	}

	buf := make([]byte, pstrlen+48)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, err
	}

	h := &Handshake{Pstr: string(buf[:pstrlen])}
	copy(h.InfoHash[:], buf[pstrlen+8:pstrlen+28]) //+8 skips past the reserved bytes
	copy(h.PeerID[:], buf[pstrlen+28:])
	return h, nil
}
