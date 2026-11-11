package tracker

import (
	"encoding/binary"
	"net"
	"strings"
	"testing"

	"github.com/VaibhavSingh79/gotorrent/torrent"
)

//a stand-in tracker so the test exercises the real packets, not a mock of them
//it answers connect then announce, and hands back whatever it was given

func fakeUDPTracker(t *testing.T, connID uint64, peers []byte, forceError string) string {
	t.Helper()

	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening: %v", err)
	}
	t.Cleanup(func() { pc.Close() })

	go func() {
		buf := make([]byte, 2048)
		for {
			n, addr, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			req := buf[:n]
			if n < 16 {
				continue
			}

			action := binary.BigEndian.Uint32(req[8:12])
			txID := binary.BigEndian.Uint32(req[12:16])

			if forceError != "" {
				res := make([]byte, 8+len(forceError))
				binary.BigEndian.PutUint32(res[0:4], actionError)
				binary.BigEndian.PutUint32(res[4:8], txID)
				copy(res[8:], forceError)
				pc.WriteTo(res, addr)
				continue
			}

			switch action {
			case actionConnect:
				res := make([]byte, 16)
				binary.BigEndian.PutUint32(res[0:4], actionConnect)
				binary.BigEndian.PutUint32(res[4:8], txID)
				binary.BigEndian.PutUint64(res[8:16], connID)
				pc.WriteTo(res, addr)

			case actionAnnounce:
				//the client must echo the connection id it was given
				if got := binary.BigEndian.Uint64(req[0:8]); got != connID {
					continue
				}
				res := make([]byte, 20+len(peers))
				binary.BigEndian.PutUint32(res[0:4], actionAnnounce)
				binary.BigEndian.PutUint32(res[4:8], txID)
				binary.BigEndian.PutUint32(res[8:12], 1800) //interval
				copy(res[20:], peers)
				pc.WriteTo(res, addr)
			}
		}
	}()

	return "udp://" + pc.LocalAddr().String() + "/announce"
}

func testTorrent() *torrent.TorrentFile {
	return &torrent.TorrentFile{
		InfoHash: [20]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20},
		Length:   1024,
	}
}

func TestAnnounceUDP(t *testing.T) {
	peers := []byte{
		127, 0, 0, 1, 0x1A, 0xE1, // 127.0.0.1:6881
		10, 0, 0, 7, 0x1A, 0xE9, // 10.0.0.7:6889
	}
	url := fakeUDPTracker(t, 0xDEADBEEFCAFE, peers, "")

	res, err := announceUDP(url, testTorrent(), [20]byte{'a'}, 6881)
	if err != nil {
		t.Fatalf("announceUDP: %v", err)
	}
	if res.Interval != 1800 {
		t.Errorf("Interval = %d, want 1800", res.Interval)
	}
	if len(res.Peers) != 2 {
		t.Fatalf("got %d peers, want 2", len(res.Peers))
	}
	if got := res.Peers[0].String(); got != "127.0.0.1:6881" {
		t.Errorf("Peers[0] = %s, want 127.0.0.1:6881", got)
	}
	if got := res.Peers[1].String(); got != "10.0.0.7:6889" {
		t.Errorf("Peers[1] = %s, want 10.0.0.7:6889", got)
	}
}

func TestAnnounceUDPSurfacesTrackerError(t *testing.T) {
	url := fakeUDPTracker(t, 1, nil, "torrent not registered")

	_, err := announceUDP(url, testTorrent(), [20]byte{'a'}, 6881)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "torrent not registered") {
		t.Errorf("error = %v, want it to mention the tracker's reason", err)
	}
}

//the announce request is a fixed 98 byte layout, so a wrong offset breaks every real tracker

func TestUDPAnnounceRequestLayout(t *testing.T) {
	var got []byte
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening: %v", err)
	}
	defer pc.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 2048)
		for {
			n, addr, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			if binary.BigEndian.Uint32(buf[8:12]) == actionConnect {
				res := make([]byte, 16)
				binary.BigEndian.PutUint32(res[4:8], binary.BigEndian.Uint32(buf[12:16]))
				binary.BigEndian.PutUint64(res[8:16], 42)
				pc.WriteTo(res, addr)
				continue
			}
			got = append([]byte(nil), buf[:n]...)
			return
		}
	}()

	url := "udp://" + pc.LocalAddr().String() + "/announce"
	tf := testTorrent()
	announceUDP(url, tf, [20]byte{'p'}, 6881) //will time out waiting for a reply, that's fine
	<-done

	if len(got) != 98 {
		t.Fatalf("announce request is %d bytes, want 98", len(got))
	}
	if cid := binary.BigEndian.Uint64(got[0:8]); cid != 42 {
		t.Errorf("connection id = %d, want the 42 from connect", cid)
	}
	if a := binary.BigEndian.Uint32(got[8:12]); a != actionAnnounce {
		t.Errorf("action = %d, want %d", a, actionAnnounce)
	}
	if !equalBytes(got[16:36], tf.InfoHash[:]) {
		t.Error("info hash is not at offset 16")
	}
	if got[36] != 'p' {
		t.Error("peer id is not at offset 36")
	}
	if left := binary.BigEndian.Uint64(got[64:72]); left != 1024 {
		t.Errorf("left = %d at offset 64, want 1024", left)
	}
	if nw := binary.BigEndian.Uint32(got[92:96]); nw != udpNumWant {
		t.Errorf("num_want = %d at offset 92, want %d", nw, udpNumWant)
	}
	if p := binary.BigEndian.Uint16(got[96:98]); p != 6881 {
		t.Errorf("port = %d at offset 96, want 6881", p)
	}
}

func equalBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
