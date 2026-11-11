package tracker

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"math/big"
	"net"
	"net/url"
	"time"

	"github.com/VaibhavSingh79/gotorrent/peer"
	"github.com/VaibhavSingh79/gotorrent/torrent"
)

//BEP 15. UDP trackers don't do bencode at all, it's all fixed size binary fields
//I can't just announce either, first I trade this magic number for a connection id

const udpProtocolID = 0x41727101980

const (
	actionConnect  = 0
	actionAnnounce = 1
	actionError    = 3
)

const udpTimeout = 10 * time.Second

//shortcut: one connect + one announce per tracker, no BEP 15 retry ladder (15*2^n seconds).
//Upgrade if UDP trackers start timing out, since a dropped packet currently kills the whole announce.

func announceUDP(announceURL string, t *torrent.TorrentFile, peerID [20]byte, port uint16) (*Response, error) {
	u, err := url.Parse(announceURL)
	if err != nil {
		return nil, fmt.Errorf("invalid tracker URL %q: %w", announceURL, err)
	}

	conn, err := net.DialTimeout("udp", u.Host, udpTimeout)
	if err != nil {
		return nil, fmt.Errorf("dialing %s: %w", u.Host, err)
	}
	defer conn.Close()

	//one deadline for the whole exchange, UDP gives me no other way to know it died
	if err := conn.SetDeadline(time.Now().Add(udpTimeout)); err != nil {
		return nil, err
	}

	connID, err := udpConnect(conn)
	if err != nil {
		return nil, err
	}

	return udpAnnounce(conn, connID, t, peerID, port)
}

//the transaction id is mine, the tracker echoes it back so I can tell my reply from a stray packet

func newTransactionID() (uint32, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1<<32))
	if err != nil {
		return 0, fmt.Errorf("generating transaction id: %w", err)
	}
	return uint32(n.Uint64()), nil
}

//connect request is 16 bytes: <8 protocol id><4 action=0><4 transaction id>
//reply is 16 bytes: <4 action=0><4 transaction id><8 connection id>

func udpConnect(conn net.Conn) (uint64, error) {
	txID, err := newTransactionID()
	if err != nil {
		return 0, err
	}

	req := make([]byte, 16)
	binary.BigEndian.PutUint64(req[0:8], udpProtocolID)
	binary.BigEndian.PutUint32(req[8:12], actionConnect)
	binary.BigEndian.PutUint32(req[12:16], txID)

	res, err := udpRoundTrip(conn, req, 16, txID)
	if err != nil {
		return 0, fmt.Errorf("udp connect: %w", err)
	}
	if action := binary.BigEndian.Uint32(res[0:4]); action != actionConnect {
		return 0, fmt.Errorf("udp connect: tracker replied with action %d, want %d", action, actionConnect)
	}

	return binary.BigEndian.Uint64(res[8:16]), nil
}

//announce request is 98 bytes, all of it fixed width, in this exact order
//reply is <4 action=1><4 transaction id><4 interval><4 leechers><4 seeders> then the usual 6 bytes per peer

func udpAnnounce(conn net.Conn, connID uint64, t *torrent.TorrentFile, peerID [20]byte, port uint16) (*Response, error) {
	txID, err := newTransactionID()
	if err != nil {
		return nil, err
	}

	req := make([]byte, 98)
	binary.BigEndian.PutUint64(req[0:8], connID)
	binary.BigEndian.PutUint32(req[8:12], actionAnnounce)
	binary.BigEndian.PutUint32(req[12:16], txID)
	copy(req[16:36], t.InfoHash[:])
	copy(req[36:56], peerID[:])
	//downloaded, left, uploaded. I only fill in left, the other two stay zero
	binary.BigEndian.PutUint64(req[64:72], uint64(t.Length))
	//event 0 = none, then my ip (0 means "use the address you see me from") and a key I don't use
	binary.BigEndian.PutUint32(req[92:96], udpNumWant) //how many peers I want back
	binary.BigEndian.PutUint16(req[96:98], port)

	//20 byte header plus 6 bytes per peer, so this buffer holds every peer I asked for
	res, err := udpRoundTrip(conn, req, 20, txID)
	if err != nil {
		return nil, fmt.Errorf("udp announce: %w", err)
	}
	if action := binary.BigEndian.Uint32(res[0:4]); action != actionAnnounce {
		return nil, fmt.Errorf("udp announce: tracker replied with action %d, want %d", action, actionAnnounce)
	}

	peers, err := peer.Unmarshal(res[20:])
	if err != nil {
		return nil, err
	}

	return &Response{
		Interval: int64(binary.BigEndian.Uint32(res[8:12])),
		Peers:    peers,
	}, nil
}

const udpNumWant = 200

//sends req, reads one datagram back, and checks it's actually mine before handing it over
//minLen is how many bytes the caller needs to parse its header without going out of bounds

func udpRoundTrip(conn net.Conn, req []byte, minLen int, txID uint32) ([]byte, error) {
	if _, err := conn.Write(req); err != nil {
		return nil, err
	}

	buf := make([]byte, 20+udpNumWant*6)
	n, err := conn.Read(buf)
	if err != nil {
		return nil, err
	}
	res := buf[:n]

	//an error reply is action 3 and the rest is a plain text message, worth surfacing
	if n >= 8 && binary.BigEndian.Uint32(res[0:4]) == actionError {
		return nil, fmt.Errorf("tracker refused: %s", string(res[8:]))
	}
	if n < minLen {
		return nil, fmt.Errorf("reply is %d bytes, want at least %d", n, minLen)
	}
	//a late reply to an earlier request would otherwise look like a valid answer
	if got := binary.BigEndian.Uint32(res[4:8]); got != txID {
		return nil, fmt.Errorf("transaction id %d does not match mine (%d)", got, txID)
	}

	return res, nil
}
