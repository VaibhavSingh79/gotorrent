package main

import (
	"crypto/rand"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/VaibhavSingh79/gotorrent/download"
	"github.com/VaibhavSingh79/gotorrent/torrent"
	"github.com/VaibhavSingh79/gotorrent/tracker"
)

func main() {
	out := flag.String("out", ".", "directory to download into")
	port := flag.Int("port", 6881, "port to advertise to the tracker")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: gotorrent [flags] <file.torrent>\n\nflags:\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	if flag.NArg() != 1 {
		flag.Usage()
		os.Exit(2)
	}
	if *port < 1 || *port > 65535 {
		fmt.Fprintf(os.Stderr, "error: port %d is out of range\n", *port)
		os.Exit(2)
	}

	if err := run(flag.Arg(0), *out, uint16(*port)); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(torrentPath, outDir string, port uint16) error {
	t, err := torrent.Open(torrentPath)
	if err != nil {
		return err
	}

	fmt.Printf("name:        %s\n", t.Name)
	fmt.Printf("info hash:   %x\n", t.InfoHash)
	fmt.Printf("size:        %s\n", humanBytes(t.Length))
	fmt.Printf("pieces:      %d x %s\n", len(t.PieceHashes), humanBytes(t.PieceLength))
	if t.IsMultiFile() {
		fmt.Printf("files:       %d\n", len(t.Files))
	}
	fmt.Printf("trackers:    %d\n\n", len(t.AnnounceList))

	st, err := torrent.NewStorage(t, outDir)
	if err != nil {
		return err
	}
	defer st.Close()

	//checking the disk before the tracker, because if it's already finished
	//there's no reason to announce at all
	var done []bool
	var have int
	if st.Existed() {
		fmt.Println("found existing files, checking what is already downloaded...")
		done, have, err = download.Completed(t, st, func(scanned, total int) {
			if scanned == total || scanned%500 == 0 {
				fmt.Printf("\r  checked %d/%d pieces", scanned, total)
			}
		})
		if err != nil {
			return fmt.Errorf("checking existing files: %w", err)
		}
		fmt.Printf("\r  %d of %d pieces already done (%s)\n\n",
			have, len(t.PieceHashes), humanBytes(int64(have)*t.PieceLength))
	}

	start := time.Now()

	if have < len(t.PieceHashes) {
		peerID, err := newPeerID()
		if err != nil {
			return err
		}

		fmt.Println("announcing to tracker...")
		res, err := tracker.Announce(t, peerID, port)
		if err != nil {
			return fmt.Errorf("could not get peers: %w", err)
		}
		fmt.Printf("got %d peers\n\n", len(res.Peers))

		lastPrint := time.Now()
		err = download.Run(t, res.Peers, peerID, st, done, func(p download.Progress) {
			//throttling so I'm not writing to the terminal thousands of times a second
			if p.Completed != p.Total && time.Since(lastPrint) < 500*time.Millisecond {
				return
			}
			lastPrint = time.Now()
			pct := float64(p.Completed) / float64(p.Total) * 100
			bytesDone := int64(p.Completed) * t.PieceLength
			if bytesDone > t.Length {
				bytesDone = t.Length
			}
			fmt.Printf("\r%5.1f%%  %s / %s  (%d/%d pieces, %d peers)      ",
				pct, humanBytes(bytesDone), humanBytes(t.Length), p.Completed, p.Total, p.ActivePeers)
		})
		fmt.Println()
		if err != nil {
			return err
		}
	}

	if err := st.Close(); err != nil {
		return fmt.Errorf("closing output files: %w", err)
	}

	dest := filepath.Join(outDir, t.Name)
	fmt.Printf("\ndone in %s -> %s\n", time.Since(start).Round(time.Second), dest)
	return nil
}

//the 20 byte id I announce under, "-GT0001-" is the usual client prefix shape, rest is random

func newPeerID() ([20]byte, error) {
	var id [20]byte
	copy(id[:], "-GT0001-")
	if _, err := rand.Read(id[8:]); err != nil {
		return id, fmt.Errorf("generating peer id: %w", err)
	}
	return id, nil
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
