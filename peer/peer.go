package peer

import (
	"encoding/binary"
	"fmt"
	"net"
	"strconv"
)

type Peer struct {
	IP   net.IP
	Port uint16
}

func (p Peer) String() string {
	return net.JoinHostPort(p.IP.String(), strconv.Itoa(int(p.Port)))
}

//the tracker packs peers as 6 bytes each, 4 for the ipv4 address and 2 for the port
//the port is big endian, same as everything else in this protocol

func Unmarshal(peersBin []byte) ([]Peer, error) {
	const peerSize = 6
	if len(peersBin)%peerSize != 0 {
		return nil, fmt.Errorf("malformed compact peer list: %d bytes is not a multiple of %d", len(peersBin), peerSize)
	}

	peers := make([]Peer, 0, len(peersBin)/peerSize)
	for i := 0; i+peerSize <= len(peersBin); i += peerSize {
		peers = append(peers, Peer{
			//copying because net.IP keeps the slice, and I don't want it aliasing the buffer
			IP:   net.IP(append([]byte(nil), peersBin[i:i+4]...)),
			Port: binary.BigEndian.Uint16(peersBin[i+4 : i+6]),
		})
	}
	return peers, nil
}
