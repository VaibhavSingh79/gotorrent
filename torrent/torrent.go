package torrent

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/VaibhavSingh79/gotorrent/bencode"
)

type File struct {
	Length int64
	Path   string //relative, already cleaned up so it can't escape the output dir
}

//everything I need out of the .torrent, pulled out of the bencode dict once
//so the rest of the program never has to deal with map[string]any again

type TorrentFile struct {
	Announce     string
	AnnounceList []string //every tracker flattened into one list, Announce first, no duplicates
	InfoHash     [20]byte
	PieceHashes  [][20]byte
	PieceLength  int64
	Length       int64 //total across all files, not the length of any one file
	Name         string
	Files        []File //nil when it's a single file torrent
}

func Open(path string) (*TorrentFile, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("opening torrent file: %w", err)
	}
	defer f.Close()

	raw, err := bencode.Decode(bufio.NewReader(f))
	if err != nil {
		return nil, fmt.Errorf("decoding torrent file: %w", err)
	}

	dict, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("torrent file top level is %T, want a dictionary", raw)
	}

	return Parse(dict)
}

//a .torrent is untrusted input, someone else made it, so everything in here
//gets checked before I believe it

func Parse(dict map[string]any) (*TorrentFile, error) {
	t := &TorrentFile{}

	t.Announce, _ = dictString(dict, "announce") //not always there, announce-list can carry it instead

	seen := map[string]bool{}
	addTracker := func(u string) {
		u = strings.TrimSpace(u)
		if u == "" || seen[u] {
			return
		}
		seen[u] = true
		t.AnnounceList = append(t.AnnounceList, u)
	}
	addTracker(t.Announce)

	//announce-list is a list of tiers and each tier is a list of urls, so it's nested twice
	if tiers, ok := dict["announce-list"].([]any); ok {
		for _, tier := range tiers {
			urls, ok := tier.([]any)
			if !ok {
				continue
			}
			for _, u := range urls {
				if s, ok := u.(string); ok {
					addTracker(s)
				}
			}
		}
	}
	if len(t.AnnounceList) == 0 {
		return nil, fmt.Errorf("torrent has no tracker URL")
	}

	infoHash, err := bencode.InfoHash(dict)
	if err != nil {
		return nil, err
	}
	t.InfoHash = infoHash

	info, ok := dict["info"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("torrent has no \"info\" dictionary")
	}

	if t.Name, err = dictString(info, "name"); err != nil {
		return nil, err
	}
	//the name becomes a file or folder name on my disk, so it can't be allowed to contain a path
	t.Name = sanitizeSegment(t.Name)
	if t.Name == "" {
		return nil, fmt.Errorf("torrent has an empty or unsafe name")
	}

	if t.PieceLength, err = dictInt(info, "piece length"); err != nil {
		return nil, err
	}
	if t.PieceLength <= 0 {
		return nil, fmt.Errorf("invalid piece length %d", t.PieceLength)
	}

	//"pieces" is every piece's 20 byte SHA-1 glued together, so I chop it back up
	pieces, err := dictString(info, "pieces")
	if err != nil {
		return nil, err
	}
	if len(pieces)%20 != 0 {
		return nil, fmt.Errorf("pieces field is %d bytes, not a multiple of 20", len(pieces))
	}
	t.PieceHashes = make([][20]byte, len(pieces)/20)
	for i := range t.PieceHashes {
		copy(t.PieceHashes[i][:], pieces[i*20:(i+1)*20])
	}

	//either "length" for one file, or "files" for many, never both
	if rawFiles, ok := info["files"].([]any); ok {
		for i, entry := range rawFiles {
			fd, ok := entry.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("files[%d] is %T, want a dictionary", i, entry)
			}
			length, err := dictInt(fd, "length")
			if err != nil {
				return nil, fmt.Errorf("files[%d]: %w", i, err)
			}
			rawPath, ok := fd["path"].([]any)
			if !ok {
				return nil, fmt.Errorf("files[%d] has no path list", i)
			}
			parts := make([]string, 0, len(rawPath))
			for _, p := range rawPath {
				s, ok := p.(string)
				if !ok {
					return nil, fmt.Errorf("files[%d]: path element is %T, want string", i, p)
				}
				parts = append(parts, s)
			}
			rel := sanitizePath(parts)
			if rel == "" {
				return nil, fmt.Errorf("files[%d] has an empty or unsafe path", i)
			}
			t.Files = append(t.Files, File{Length: length, Path: rel})
			t.Length += length //total is just the sum, the files sit end to end
		}
		if len(t.Files) == 0 {
			return nil, fmt.Errorf("torrent has an empty files list")
		}
	} else {
		if t.Length, err = dictInt(info, "length"); err != nil {
			return nil, err
		}
	}

	if t.Length <= 0 {
		return nil, fmt.Errorf("invalid total length %d", t.Length)
	}

	//if the hash count doesn't match the size, the file is lying to me and nothing downstream will work
	wantPieces := (t.Length + t.PieceLength - 1) / t.PieceLength
	if int64(len(t.PieceHashes)) != wantPieces {
		return nil, fmt.Errorf("have %d piece hashes but %d pieces of %d bytes are needed for %d bytes",
			len(t.PieceHashes), wantPieces, t.PieceLength, t.Length)
	}

	return t, nil
}

//where piece index lives in the end to end layout of all the files

func (t *TorrentFile) PieceBounds(index int) (begin, end int64) {
	begin = int64(index) * t.PieceLength
	end = begin + t.PieceLength
	if end > t.Length {
		end = t.Length //the last piece is almost always short
	}
	return begin, end
}

func (t *TorrentFile) PieceSize(index int) int {
	begin, end := t.PieceBounds(index)
	return int(end - begin)
}

func (t *TorrentFile) IsMultiFile() bool { return len(t.Files) > 0 }

func dictString(d map[string]any, key string) (string, error) {
	v, ok := d[key]
	if !ok {
		return "", fmt.Errorf("missing key %q", key)
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("key %q is %T, want string", key, v)
	}
	return s, nil
}

func dictInt(d map[string]any, key string) (int64, error) {
	v, ok := d[key]
	if !ok {
		return 0, fmt.Errorf("missing key %q", key)
	}
	n, ok := v.(int64) //my decoder always gives int64, never int
	if !ok {
		return 0, fmt.Errorf("key %q is %T, want integer", key, v)
	}
	return n, nil
}

//a torrent can put "../../etc/passwd" in a path, so I flatten every component
//down to a plain name before it goes anywhere near the filesystem

func sanitizeSegment(s string) string {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, "\x00", "")
	s = strings.ReplaceAll(s, "/", "_")
	s = strings.ReplaceAll(s, "\\", "_") //windows separator, otherwise it slips through on linux
	if s == "." || s == ".." {
		return ""
	}
	return s
}

func sanitizePath(parts []string) string {
	clean := make([]string, 0, len(parts))
	for _, p := range parts {
		if seg := sanitizeSegment(p); seg != "" {
			clean = append(clean, seg)
		}
	}
	if len(clean) == 0 {
		return ""
	}
	return filepath.Join(clean...)
}
