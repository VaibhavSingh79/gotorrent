package torrent

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/VaibhavSingh79/gotorrent/bencode"
)

func parseRaw(t *testing.T, raw string) (*TorrentFile, error) {
	t.Helper()
	v, err := bencode.Decode(bufio.NewReader(strings.NewReader(raw)))
	if err != nil {
		t.Fatalf("decoding fixture: %v", err)
	}
	return Parse(v.(map[string]any))
}

//1024 bytes in 256-byte pieces = 4 pieces = 80 bytes of hashes.

const singleFile = "d8:announce18:http://tracker/ann4:infod6:lengthi1024e4:name8:test.iso12:piece lengthi256e6:pieces80:" +
	"aaaaaaaaaaaaaaaaaaaabbbbbbbbbbbbbbbbbbbbccccccccccccccccccccddddddddddddddddddddee"

func TestParseSingleFile(t *testing.T) {
	tf, err := parseRaw(t, singleFile)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if tf.Name != "test.iso" {
		t.Errorf("Name = %q, want test.iso", tf.Name)
	}
	if tf.Length != 1024 {
		t.Errorf("Length = %d, want 1024", tf.Length)
	}
	if tf.PieceLength != 256 {
		t.Errorf("PieceLength = %d, want 256", tf.PieceLength)
	}
	if len(tf.PieceHashes) != 4 {
		t.Fatalf("got %d piece hashes, want 4", len(tf.PieceHashes))
	}
	if string(tf.PieceHashes[1][:]) != strings.Repeat("b", 20) {
		t.Errorf("PieceHashes[1] = %q", tf.PieceHashes[1])
	}
	if tf.IsMultiFile() {
		t.Error("IsMultiFile() = true, want false")
	}
	if len(tf.AnnounceList) != 1 || tf.AnnounceList[0] != "http://tracker/ann" {
		t.Errorf("AnnounceList = %v", tf.AnnounceList)
	}
}

func TestPieceBoundsAndSize(t *testing.T) {
	//1000 bytes in 256-byte pieces: three full pieces and a 232-byte tail.
	raw := strings.Replace(singleFile, "6:lengthi1024e", "6:lengthi1000e", 1)
	tf, err := parseRaw(t, raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	want := []struct{ begin, end int64 }{
		{0, 256}, {256, 512}, {512, 768}, {768, 1000},
	}
	for i, w := range want {
		begin, end := tf.PieceBounds(i)
		if begin != w.begin || end != w.end {
			t.Errorf("PieceBounds(%d) = (%d, %d), want (%d, %d)", i, begin, end, w.begin, w.end)
		}
		if got, wantSize := tf.PieceSize(i), int(w.end-w.begin); got != wantSize {
			t.Errorf("PieceSize(%d) = %d, want %d", i, got, wantSize)
		}
	}
}

func TestParseMultiFile(t *testing.T) {
	raw := "d8:announce18:http://tracker/ann4:infod5:filesld6:lengthi600e4:pathl3:dir5:a.txteed6:lengthi424e4:pathl5:b.txteee" +
		"4:name7:bundle112:piece lengthi256e6:pieces80:" +
		"aaaaaaaaaaaaaaaaaaaabbbbbbbbbbbbbbbbbbbbccccccccccccccccccccddddddddddddddddddddee"

	tf, err := parseRaw(t, raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !tf.IsMultiFile() {
		t.Fatal("IsMultiFile() = false, want true")
	}
	if len(tf.Files) != 2 {
		t.Fatalf("got %d files, want 2", len(tf.Files))
	}
	if tf.Length != 1024 {
		t.Errorf("total Length = %d, want 1024", tf.Length)
	}
	if got := tf.Files[0].Path; got != filepath.Join("dir", "a.txt") {
		t.Errorf("Files[0].Path = %q", got)
	}
	if tf.Files[1].Path != "b.txt" || tf.Files[1].Length != 424 {
		t.Errorf("Files[1] = %+v", tf.Files[1])
	}
}

func TestParseRejectsBadTorrents(t *testing.T) {
	cases := map[string]string{
		"no info":          "d8:announce18:http://tracker/anne",
		"no tracker":       strings.Replace(singleFile, "8:announce18:http://tracker/ann", "", 1),
		"pieces not /20":   strings.Replace(singleFile, "6:pieces80:", "6:pieces79:", 1),
		"hash count wrong": strings.Replace(singleFile, "6:lengthi1024e", "6:lengthi99999e", 1),
		"zero piece len":   strings.Replace(singleFile, "12:piece lengthi256e", "12:piece lengthi0e", 1),
	}
	for name, raw := range cases {
		//A decode failure counts as a rejection too, so don't use the
		//fatal helper here.
		v, err := bencode.Decode(bufio.NewReader(strings.NewReader(raw)))
		if err != nil {
			continue // malformed bencode, correctly rejected
		}
		dict, ok := v.(map[string]any)
		if !ok {
			continue
		}
		if _, err := Parse(dict); err == nil {
			t.Errorf("%s: Parse succeeded, want an error", name)
		}
	}
}

//A malicious torrent must not be able to write outside the output directory.

func TestParseSanitizesPaths(t *testing.T) {
	raw := "d8:announce18:http://tracker/ann4:infod5:filesld6:lengthi1024e4:pathl2:..2:..7:etc.txteee" +
		"4:name7:bundle112:piece lengthi256e6:pieces80:" +
		"aaaaaaaaaaaaaaaaaaaabbbbbbbbbbbbbbbbbbbbccccccccccccccccccccddddddddddddddddddddee"

	tf, err := parseRaw(t, raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	got := tf.Files[0].Path
	if strings.Contains(got, "..") {
		t.Errorf("Files[0].Path = %q, traversal was not stripped", got)
	}
	if got != "etc.txt" {
		t.Errorf("Files[0].Path = %q, want etc.txt", got)
	}
}

func TestStorageSingleFile(t *testing.T) {
	tf, err := parseRaw(t, singleFile)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	dir := t.TempDir()
	st, err := NewStorage(tf, dir)
	if err != nil {
		t.Fatalf("NewStorage: %v", err)
	}
	defer st.Close()

	//Write the last piece first to prove offsets work out of order.
	if err := st.WriteAt([]byte(strings.Repeat("D", 256)), 768); err != nil {
		t.Fatalf("WriteAt: %v", err)
	}
	if err := st.WriteAt([]byte(strings.Repeat("A", 768)), 0); err != nil {
		t.Fatalf("WriteAt: %v", err)
	}
	st.Close()

	got, err := os.ReadFile(filepath.Join(dir, "test.iso"))
	if err != nil {
		t.Fatalf("reading output: %v", err)
	}
	want := strings.Repeat("A", 768) + strings.Repeat("D", 256)
	if string(got) != want {
		t.Errorf("file contents differ (len %d, want %d)", len(got), len(want))
	}
}

//The important case: one piece straddling a file boundary must be split
//across both files correctly.

func TestStorageWriteAcrossFileBoundary(t *testing.T) {
	raw := "d8:announce18:http://tracker/ann4:infod5:filesld6:lengthi600e4:pathl5:a.txteed6:lengthi424e4:pathl5:b.txteee" +
		"4:name7:bundle112:piece lengthi256e6:pieces80:" +
		"aaaaaaaaaaaaaaaaaaaabbbbbbbbbbbbbbbbbbbbccccccccccccccccccccddddddddddddddddddddee"

	tf, err := parseRaw(t, raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	dir := t.TempDir()
	st, err := NewStorage(tf, dir)
	if err != nil {
		t.Fatalf("NewStorage: %v", err)
	}

	//Piece 2 covers bytes 512..768, crossing the boundary at 600.
	if err := st.WriteAt([]byte(strings.Repeat("X", 256)), 512); err != nil {
		t.Fatalf("WriteAt: %v", err)
	}
	st.Close()

	a, err := os.ReadFile(filepath.Join(dir, "bundle1", "a.txt"))
	if err != nil {
		t.Fatalf("reading a.txt: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "bundle1", "b.txt"))
	if err != nil {
		t.Fatalf("reading b.txt: %v", err)
	}

	if len(a) != 600 || len(b) != 424 {
		t.Fatalf("file sizes = %d, %d; want 600, 424", len(a), len(b))
	}
	//88 bytes of X at the end of a.txt, 168 at the start of b.txt.
	if got := string(a[512:600]); got != strings.Repeat("X", 88) {
		t.Errorf("a.txt tail = %q", got)
	}
	if got := string(b[:168]); got != strings.Repeat("X", 168) {
		t.Errorf("b.txt head = %q", got)
	}
	if b[168] != 0 {
		t.Error("b.txt was written past the end of the piece")
	}
}

func TestStorageRejectsOverrun(t *testing.T) {
	tf, err := parseRaw(t, singleFile)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	dir := t.TempDir()
	st, err := NewStorage(tf, dir)
	if err != nil {
		t.Fatalf("NewStorage: %v", err)
	}
	defer st.Close()

	if err := st.WriteAt(make([]byte, 100), 1000); err == nil {
		t.Error("expected an error writing past the end of the torrent")
	}
}
