package torrent

import (
	"fmt"
	"os"
	"path/filepath"
)

//pieces arrive in whatever order peers hand them to me, so I can't just append
//instead I lay all the files out end to end and let callers write at one global offset,
//and this works out which file (or files) that offset lands in

type Storage struct {
	files   []storedFile
	existed bool //something was already on disk, so a resume scan is worth the time
}

type storedFile struct {
	f      *os.File
	offset int64 //where this file starts in the end to end layout
	length int64
}

func NewStorage(t *TorrentFile, dir string) (*Storage, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("creating output directory: %w", err)
	}

	s := &Storage{}

	open := func(path string, length int64, offset int64) error {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return fmt.Errorf("creating directory for %s: %w", path, err)
		}
		//checking before I open, because O_CREATE makes it exist either way
		if info, err := os.Stat(path); err == nil && info.Size() > 0 {
			s.existed = true
		}
		//no O_TRUNC here, that's the whole point, an existing file keeps its data for the resume scan
		f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
		if err != nil {
			return fmt.Errorf("creating %s: %w", path, err)
		}
		//sizing it up front so a write at a high offset has somewhere to land
		if err := f.Truncate(length); err != nil {
			f.Close()
			return fmt.Errorf("preallocating %s: %w", path, err)
		}
		s.files = append(s.files, storedFile{f: f, offset: offset, length: length})
		return nil
	}

	if t.IsMultiFile() {
		root := filepath.Join(dir, t.Name)
		var offset int64
		for _, tf := range t.Files {
			if err := open(filepath.Join(root, tf.Path), tf.Length, offset); err != nil {
				s.Close()
				return nil, err
			}
			offset += tf.Length //next file starts where this one ends
		}
	} else {
		if err := open(filepath.Join(dir, t.Name), t.Length, 0); err != nil {
			s.Close()
			return nil, err
		}
	}

	return s, nil
}

func (s *Storage) Existed() bool { return s.existed }

//a piece can straddle a file boundary, so one write can turn into two

func (s *Storage) WriteAt(p []byte, off int64) error {
	return s.split(p, off, func(sf storedFile, b []byte, at int64) error {
		_, err := sf.f.WriteAt(b, at)
		return err
	})
}

//same splitting the other way round, this is what the resume scan reads back through

func (s *Storage) ReadAt(p []byte, off int64) error {
	return s.split(p, off, func(sf storedFile, b []byte, at int64) error {
		_, err := sf.f.ReadAt(b, at)
		return err
	})
}

//walks p across however many files it covers and calls do once per file

func (s *Storage) split(p []byte, off int64, do func(sf storedFile, b []byte, at int64) error) error {
	start := off
	for _, sf := range s.files {
		if len(p) == 0 {
			break
		}
		fileEnd := sf.offset + sf.length
		if off >= fileEnd || off+int64(len(p)) <= sf.offset {
			continue //doesn't touch this file at all
		}

		at := off - sf.offset //offset within this file, not the global one
		n := int64(len(p))
		if at+n > sf.length {
			n = sf.length - at //the rest spills into the next file
		}

		if err := do(sf, p[:n], at); err != nil {
			return fmt.Errorf("at offset %d: %w", off, err)
		}

		p = p[n:]
		off += n
	}

	if len(p) > 0 {
		return fmt.Errorf("%d bytes past the end of the torrent at offset %d", len(p), start)
	}
	return nil
}

func (s *Storage) Close() error {
	var firstErr error
	for _, sf := range s.files {
		if err := sf.f.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	s.files = nil
	return firstErr
}
