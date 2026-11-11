# gotorrent

A BitTorrent client written from scratch in Go. No dependencies outside the standard library.

```
go build -o gotorrent .
./gotorrent -out ~/Downloads ubuntu-26.04-desktop-amd64.iso.torrent
```

```
  -out string    directory to download into (default ".")
  -port int      port to advertise to the tracker (default 6881)
```

## How it is put together

| Package | What it does |
| --- | --- |
| `bencode` | Decodes and encodes bencode, and computes the info hash |
| `torrent` | Parses the `.torrent` metainfo, and writes finished pieces to disk |
| `tracker` | Announces over HTTP(S) and UDP, returns peers |
| `peer` | Handshake, message framing, bitfield, one client per connection |
| `download` | Worker pool that fetches, verifies and assembles pieces |

Each peer gets a goroutine pulling pieces off a shared queue. Block requests are
pipelined five deep, because one request per round trip is unusably slow over
real latency. Every piece is checked against its SHA-1 from the metainfo before
it is written, so a peer sending garbage costs a retry and nothing else.

## What works

- Single and multi-file torrents, including pieces that straddle a file boundary
- HTTP, HTTPS and UDP trackers (BEP 15), tried in turn until one returns peers
- Resume: a restart hashes what is already on disk and downloads only the rest.
  There is no state file, the data is the state, so nothing can go stale or lie
  after a hard kill.
- Torrent files are treated as untrusted input: path traversal is stripped,
  message sizes are capped, and every offset a peer sends is bounds checked.

## What is missing

- Download only. It never uploads and accepts no incoming connections.
- No DHT or magnet links, so a `.torrent` file with a working tracker is required.
- No re-announce. It uses the peer list from the first announce and does not
  replace peers that drop.
- UDP trackers get one connect and one announce, with no BEP 15 retry ladder,
  so a dropped packet fails that tracker.
- The resume scan reads the whole file on every restart, which is slow for large
  torrents.

## Tests

```
go test ./...
```

The UDP tests run a real tracker in-process rather than mocking one, and assert
the 98 byte announce layout field by field.
