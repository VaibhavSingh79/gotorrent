package peer

import (
	"bufio"
	"bytes"
	"fmt"
	"net"
	"time"
)

const (
	dialTimeout      = 5 * time.Second
	handshakeTimeout = 10 * time.Second
	bitfieldTimeout  = 15 * time.Second
)

type Client struct {
	Conn     net.Conn
	Choked   bool
	Bitfield Bitfield
	Peer     Peer
	InfoHash [20]byte
	PeerID   [20]byte

	r *bufio.Reader //same reason I used one in the decoder, byte at a time reads off a socket are brutal
}

//dials the peer, handshakes, and grabs whatever it says it has
//numPieces is here because a peer can skip the bitfield entirely, and I still need one the right size

func New(p Peer, peerID, infoHash [20]byte, numPieces int) (*Client, error) {
	conn, err := net.DialTimeout("tcp", p.String(), dialTimeout)
	if err != nil {
		return nil, fmt.Errorf("dialing %s: %w", p, err)
	}

	c := &Client{
		Conn:     conn,
		Choked:   true, //everyone starts choked, I can't ask for anything until they unchoke me
		Peer:     p,
		InfoHash: infoHash,
		PeerID:   peerID,
		r:        bufio.NewReader(conn),
	}

	if err := c.handshake(); err != nil {
		conn.Close()
		return nil, err
	}

	bf, err := c.recvBitfield(numPieces)
	if err != nil {
		conn.Close()
		return nil, err
	}
	c.Bitfield = bf

	return c, nil
}

func (c *Client) handshake() error {
	if err := c.Conn.SetDeadline(time.Now().Add(handshakeTimeout)); err != nil {
		return err
	}
	defer c.Conn.SetDeadline(time.Time{}) //clearing it, the download loop sets its own

	req := NewHandshake(c.InfoHash, c.PeerID)
	if _, err := c.Conn.Write(req.Serialize()); err != nil {
		return fmt.Errorf("sending handshake to %s: %w", c.Peer, err)
	}

	res, err := ReadHandshake(c.r)
	if err != nil {
		return fmt.Errorf("reading handshake from %s: %w", c.Peer, err)
	}
	//if the info hash doesn't match we're talking about different torrents, so there's no point continuing
	if !bytes.Equal(res.InfoHash[:], c.InfoHash[:]) {
		return fmt.Errorf("%s returned info hash %x, want %x", c.Peer, res.InfoHash, c.InfoHash)
	}

	return nil
}

//the bitfield is normally the first message, but not always
//some peers send keep-alives or choke first, and some skip the bitfield and just send have messages
//so I read a few messages looking for it instead of demanding it up front

func (c *Client) recvBitfield(numPieces int) (Bitfield, error) {
	if err := c.Conn.SetDeadline(time.Now().Add(bitfieldTimeout)); err != nil {
		return nil, err
	}
	defer c.Conn.SetDeadline(time.Time{})

	bf := make(Bitfield, (numPieces+7)/8) //+7 so a partial last byte still gets counted

	for i := 0; i < 10; i++ {
		msg, err := ReadMessage(c.r)
		if err != nil {
			return nil, fmt.Errorf("reading bitfield from %s: %w", c.Peer, err)
		}
		if msg == nil {
			continue //keep-alive, nothing to do
		}

		switch msg.ID {
		case MsgBitfield:
			//copying into my own slice instead of taking theirs, so the length is always what numPieces needs
			copy(bf, msg.Payload)
			return bf, nil
		case MsgHave:
			//no bitfield coming then, this peer announces one piece at a time
			if index, err := ParseHave(msg); err == nil {
				bf.SetPiece(index)
			}
			return bf, nil
		case MsgChoke:
			c.Choked = true
		case MsgUnchoke:
			c.Choked = false
		}
		//anything else is an extension message I don't speak, so I ignore it
	}

	//nothing useful arrived, but the peer can still send have messages later so I keep the connection
	return bf, nil
}

func (c *Client) Read() (*Message, error) { return ReadMessage(c.r) }

func (c *Client) SendRequest(index, begin, length int) error {
	_, err := c.Conn.Write(FormatRequest(index, begin, length).Serialize())
	return err
}

func (c *Client) SendInterested() error    { return c.send(&Message{ID: MsgInterested}) }
func (c *Client) SendNotInterested() error { return c.send(&Message{ID: MsgNotInterested}) }
func (c *Client) SendUnchoke() error       { return c.send(&Message{ID: MsgUnchoke}) }
func (c *Client) SendHave(index int) error { return c.send(FormatHave(index)) }

func (c *Client) send(m *Message) error {
	_, err := c.Conn.Write(m.Serialize())
	return err
}

func (c *Client) Close() error { return c.Conn.Close() }
