package download

import (
	"bytes"
	"crypto/sha1"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/VaibhavSingh79/gotorrent/peer"
	"github.com/VaibhavSingh79/gotorrent/torrent"
)

const (
	maxBlockSize = 16384 //peers refuse anything bigger, so a piece comes down in 16KiB chunks

	//I keep this many requests in flight at once. Asking for one block and waiting for it
	//means one round trip per 16KiB, which is unusably slow over real latency
	maxBacklog = 5

	pieceTimeout = 45 * time.Second
	stallTimeout = 2 * time.Minute
)

type pieceWork struct {
	index  int
	hash   [20]byte
	length int
}

type pieceResult struct {
	index int
	buf   []byte
}

type Progress struct {
	Completed   int
	Total       int
	ActivePeers int
}

//hashes what is already on disk so a restart doesn't download it all again
//no state file anywhere, the data itself is the state, which means it can't go stale or lie to me

func Completed(t *torrent.TorrentFile, st *torrent.Storage, onProgress func(scanned, total int)) ([]bool, int, error) {
	done := make([]bool, len(t.PieceHashes))
	have := 0

	buf := make([]byte, t.PieceLength) //reused every loop, no point allocating 24k of these
	for i := range t.PieceHashes {
		begin, end := t.PieceBounds(i)
		p := buf[:end-begin]
		if err := st.ReadAt(p, begin); err != nil {
			return nil, 0, err
		}
		//a fresh file is all zeros and won't match, so unwritten pieces just fall through
		if sha1.Sum(p) == t.PieceHashes[i] {
			done[i] = true
			have++
		}
		if onProgress != nil {
			onProgress(i+1, len(t.PieceHashes))
		}
	}

	return done, have, nil
}

//done comes from Completed, or nil for a fresh download

func Run(t *torrent.TorrentFile, peers []peer.Peer, peerID [20]byte, st *torrent.Storage, done []bool, onProgress func(Progress)) error {
	numPieces := len(t.PieceHashes)

	//buffered to hold every piece, so a worker handing one back can never block
	workQueue := make(chan *pieceWork, numPieces)
	results := make(chan *pieceResult)

	completed := 0
	for i, hash := range t.PieceHashes {
		if done != nil && done[i] {
			completed++
			continue //already on disk and verified, I'm not asking anyone for it
		}
		workQueue <- &pieceWork{index: i, hash: hash, length: t.PieceSize(i)}
	}

	if completed == numPieces {
		return nil //nothing left, so I don't need peers at all
	}
	if len(peers) == 0 {
		return fmt.Errorf("no peers to download from")
	}

	var activeWorkers int64
	for _, p := range peers {
		atomic.AddInt64(&activeWorkers, 1)
		go func(p peer.Peer) {
			defer atomic.AddInt64(&activeWorkers, -1)
			runWorker(p, t, peerID, numPieces, workQueue, results)
		}(p)
	}

	defer close(workQueue)

	for completed < numPieces {
		select {
		case res := <-results:
			begin, _ := t.PieceBounds(res.index)
			if err := st.WriteAt(res.buf, begin); err != nil {
				return err
			}
			completed++
			if onProgress != nil {
				onProgress(Progress{
					Completed:   completed,
					Total:       numPieces,
					ActivePeers: int(atomic.LoadInt64(&activeWorkers)),
				})
			}

		case <-time.After(stallTimeout):
			return fmt.Errorf("download stalled: %d of %d pieces done, %d peers still connected",
				completed, numPieces, atomic.LoadInt64(&activeWorkers))
		}

		//every worker quit, so nothing is ever going to arrive and I'd block forever
		if atomic.LoadInt64(&activeWorkers) == 0 && completed < numPieces {
			return fmt.Errorf("all peers disconnected after %d of %d pieces", completed, numPieces)
		}
	}

	return nil
}

//one of these per peer, it lives for the whole download pulling whatever it can off the queue

func runWorker(p peer.Peer, t *torrent.TorrentFile, peerID [20]byte, numPieces int, workQueue chan *pieceWork, results chan<- *pieceResult) {
	c, err := peer.New(p, peerID, t.InfoHash, numPieces)
	if err != nil {
		return //unreachable or on a different torrent, nothing to do but drop it
	}
	defer c.Close()

	if err := c.SendUnchoke(); err != nil {
		return
	}
	if err := c.SendInterested(); err != nil { //without this they'll never unchoke me
		return
	}

	for pw := range workQueue {
		if !c.Bitfield.HasPiece(pw.index) {
			workQueue <- pw //this peer can't serve it, someone else might
			continue
		}

		buf, err := downloadPiece(c, pw)
		if err != nil {
			workQueue <- pw
			return //the connection is probably dead, so this worker is done
		}

		//this is the check that makes the whole thing safe, a peer can send me anything
		if !bytes.Equal(sha1Sum(buf), pw.hash[:]) {
			workQueue <- pw //corrupt, throw it away and let another peer try
			continue
		}

		_ = c.SendHave(pw.index) //just being polite, I don't care if it fails
		results <- &pieceResult{index: pw.index, buf: buf}
	}
}

func sha1Sum(b []byte) []byte {
	sum := sha1.Sum(b)
	return sum[:]
}

//tracks one piece coming down from one peer

type pieceProgress struct {
	index      int
	client     *peer.Client
	buf        []byte
	downloaded int
	requested  int
	backlog    int
}

func (s *pieceProgress) readMessage() error {
	msg, err := s.client.Read()
	if err != nil {
		return err
	}
	if msg == nil {
		return nil //keep-alive
	}

	switch msg.ID {
	case peer.MsgUnchoke:
		s.client.Choked = false
	case peer.MsgChoke:
		s.client.Choked = true
	case peer.MsgHave:
		index, err := peer.ParseHave(msg)
		if err != nil {
			return err
		}
		s.client.Bitfield.SetPiece(index) //they picked up a piece mid-download, so now I can ask for it
	case peer.MsgPiece:
		n, err := peer.ParsePiece(s.index, s.buf, msg)
		if err != nil {
			return err
		}
		s.downloaded += n
		s.backlog--
	}
	return nil
}

func downloadPiece(c *peer.Client, pw *pieceWork) ([]byte, error) {
	state := pieceProgress{index: pw.index, client: c, buf: make([]byte, pw.length)}

	if err := c.Conn.SetDeadline(time.Now().Add(pieceTimeout)); err != nil {
		return nil, err
	}
	defer c.Conn.SetDeadline(time.Time{})

	for state.downloaded < pw.length {
		//only worth asking while they're willing to answer
		if !c.Choked {
			for state.backlog < maxBacklog && state.requested < pw.length {
				blockSize := maxBlockSize
				if pw.length-state.requested < blockSize {
					blockSize = pw.length - state.requested //last block of the piece is usually short
				}
				if err := c.SendRequest(pw.index, state.requested, blockSize); err != nil {
					return nil, err
				}
				state.backlog++
				state.requested += blockSize
			}
		}

		if err := state.readMessage(); err != nil {
			return nil, err
		}
	}

	return state.buf, nil
}
