package download

import (
	"bufio"
	"crypto/sha1"
	"strings"
	"testing"

	"github.com/VaibhavSingh79/gotorrent/bencode"
	"github.com/VaibhavSingh79/gotorrent/torrent"
)

//builds a real torrent whose piece hashes match the content I'm about to write,
//so the scan is checking against genuine SHA-1s rather than fixtures I made up

func resumeFixture(t *testing.T) (*torrent.TorrentFile, []byte) {
	t.Helper()

	const pieceLen = 256
	content := []byte(strings.Repeat("A", pieceLen) +
		strings.Repeat("B", pieceLen) +
		strings.Repeat("C", pieceLen) +
		strings.Repeat("D", 100)) //short last piece, same as a real torrent

	var hashes strings.Builder
	for off := 0; off < len(content); off += pieceLen {
		end := off + pieceLen
		if end > len(content) {
			end = len(content)
		}
		sum := sha1.Sum(content[off:end])
		hashes.Write(sum[:])
	}

	raw := "d8:announce18:http://tracker/ann4:infod6:lengthi" +
		itoa(len(content)) + "e4:name8:test.bin12:piece lengthi256e6:pieces" +
		itoa(hashes.Len()) + ":" + hashes.String() + "ee"

	v, err := bencode.Decode(bufio.NewReader(strings.NewReader(raw)))
	if err != nil {
		t.Fatalf("decoding fixture: %v", err)
	}
	tf, err := torrent.Parse(v.(map[string]any))
	if err != nil {
		t.Fatalf("parsing fixture: %v", err)
	}
	return tf, content
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func TestCompletedOnEmptyFiles(t *testing.T) {
	tf, _ := resumeFixture(t)

	st, err := torrent.NewStorage(tf, t.TempDir())
	if err != nil {
		t.Fatalf("NewStorage: %v", err)
	}
	defer st.Close()

	//a fresh file is all zeros, so nothing should look finished
	done, have, err := Completed(tf, st, nil)
	if err != nil {
		t.Fatalf("Completed: %v", err)
	}
	if have != 0 {
		t.Errorf("have = %d, want 0 on a fresh file", have)
	}
	for i, d := range done {
		if d {
			t.Errorf("piece %d reported done on a fresh file", i)
		}
	}
}

func TestCompletedFindsPartialDownload(t *testing.T) {
	tf, content := resumeFixture(t)

	st, err := torrent.NewStorage(tf, t.TempDir())
	if err != nil {
		t.Fatalf("NewStorage: %v", err)
	}
	defer st.Close()

	//writing pieces 0 and 3 only, leaving 1 and 2 as holes
	for _, i := range []int{0, 3} {
		begin, end := tf.PieceBounds(i)
		if err := st.WriteAt(content[begin:end], begin); err != nil {
			t.Fatalf("WriteAt: %v", err)
		}
	}

	done, have, err := Completed(tf, st, nil)
	if err != nil {
		t.Fatalf("Completed: %v", err)
	}
	if have != 2 {
		t.Fatalf("have = %d, want 2", have)
	}
	want := []bool{true, false, false, true}
	for i, w := range want {
		if done[i] != w {
			t.Errorf("done[%d] = %v, want %v", i, done[i], w)
		}
	}
}

//corrupt data must not be mistaken for a finished piece, or resume would skip it forever

func TestCompletedRejectsCorruptPiece(t *testing.T) {
	tf, content := resumeFixture(t)

	st, err := torrent.NewStorage(tf, t.TempDir())
	if err != nil {
		t.Fatalf("NewStorage: %v", err)
	}
	defer st.Close()

	begin, end := tf.PieceBounds(1)
	bad := append([]byte(nil), content[begin:end]...)
	bad[0] ^= 0xFF
	if err := st.WriteAt(bad, begin); err != nil {
		t.Fatalf("WriteAt: %v", err)
	}

	done, have, err := Completed(tf, st, nil)
	if err != nil {
		t.Fatalf("Completed: %v", err)
	}
	if have != 0 || done[1] {
		t.Errorf("corrupt piece 1 was accepted (have=%d, done[1]=%v)", have, done[1])
	}
}

//a finished download must need no peers at all

func TestRunReturnsEarlyWhenEverythingIsDone(t *testing.T) {
	tf, content := resumeFixture(t)

	st, err := torrent.NewStorage(tf, t.TempDir())
	if err != nil {
		t.Fatalf("NewStorage: %v", err)
	}
	defer st.Close()

	if err := st.WriteAt(content, 0); err != nil {
		t.Fatalf("WriteAt: %v", err)
	}
	done, have, err := Completed(tf, st, nil)
	if err != nil {
		t.Fatalf("Completed: %v", err)
	}
	if have != len(tf.PieceHashes) {
		t.Fatalf("have = %d, want %d", have, len(tf.PieceHashes))
	}

	//nil peers would be an error if Run tried to download anything
	if err := Run(tf, nil, [20]byte{}, st, done, nil); err != nil {
		t.Errorf("Run with everything done: %v", err)
	}
}

func TestStorageExisted(t *testing.T) {
	tf, content := resumeFixture(t)
	dir := t.TempDir()

	st, err := torrent.NewStorage(tf, dir)
	if err != nil {
		t.Fatalf("NewStorage: %v", err)
	}
	if st.Existed() {
		t.Error("Existed() = true on a brand new download")
	}
	if err := st.WriteAt(content, 0); err != nil {
		t.Fatalf("WriteAt: %v", err)
	}
	st.Close()

	//second run over the same directory must notice the data is there
	st2, err := torrent.NewStorage(tf, dir)
	if err != nil {
		t.Fatalf("NewStorage (second): %v", err)
	}
	defer st2.Close()
	if !st2.Existed() {
		t.Error("Existed() = false when the file was already on disk")
	}
}
