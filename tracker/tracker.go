package tracker

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/VaibhavSingh79/gotorrent/bencode"
	"github.com/VaibhavSingh79/gotorrent/peer"
	"github.com/VaibhavSingh79/gotorrent/torrent"
)

type Response struct {
	Interval int64
	Peers    []peer.Peer
}

var client = &http.Client{Timeout: 20 * time.Second}

//tries every tracker in announce-list until one gives me peers
//I only report the failures if they ALL fail, a dead backup tracker isn't worth printing

func Announce(t *torrent.TorrentFile, peerID [20]byte, port uint16) (*Response, error) {
	var errs []error

	for _, announceURL := range t.AnnounceList {
		var res *Response
		var err error

		switch strings.ToLower(schemeOf(announceURL)) {
		case "http", "https":
			res, err = announceHTTP(announceURL, t, peerID, port)
		case "udp":
			res, err = announceUDP(announceURL, t, peerID, port)
		default:
			continue //websocket trackers and anything else I don't speak
		}

		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", announceURL, err))
			continue
		}
		if len(res.Peers) == 0 {
			errs = append(errs, fmt.Errorf("%s: no peers", announceURL))
			continue
		}
		return res, nil
	}

	if len(errs) == 0 {
		return nil, fmt.Errorf("no usable tracker found")
	}
	return nil, errors.Join(errs...)
}

func schemeOf(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return u.Scheme
}

func announceHTTP(announceURL string, t *torrent.TorrentFile, peerID [20]byte, port uint16) (*Response, error) {
	reqURL, err := buildURL(announceURL, t, peerID, port)
	if err != nil {
		return nil, err
	}

	resp, err := client.Get(reqURL)
	if err != nil {
		return nil, fmt.Errorf("announce request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("tracker returned HTTP %s", resp.Status)
	}

	//a tracker reply is tiny, so I cap it rather than let a broken one stream forever
	body := bufio.NewReader(io.LimitReader(resp.Body, 2<<20))
	raw, err := bencode.Decode(body)
	if err != nil {
		return nil, fmt.Errorf("decoding tracker response: %w", err)
	}

	dict, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("tracker response is %T, want a dictionary", raw)
	}

	//the tracker says no by putting the reason in the dict, not by failing the HTTP request
	if reason, ok := dict["failure reason"].(string); ok {
		return nil, fmt.Errorf("tracker refused: %s", reason)
	}

	res := &Response{}
	if interval, ok := dict["interval"].(int64); ok {
		res.Interval = interval
	}

	res.Peers, err = parsePeers(dict["peers"])
	if err != nil {
		return nil, err
	}

	return res, nil
}

func buildURL(announceURL string, t *torrent.TorrentFile, peerID [20]byte, port uint16) (string, error) {
	base, err := url.Parse(announceURL)
	if err != nil {
		return "", fmt.Errorf("invalid tracker URL %q: %w", announceURL, err)
	}

	params := url.Values{
		//info_hash and peer_id are raw bytes, not text, but url.Values percent-encodes them right
		"info_hash":  []string{string(t.InfoHash[:])},
		"peer_id":    []string{string(peerID[:])},
		"port":       []string{strconv.Itoa(int(port))},
		"uploaded":   []string{"0"},
		"downloaded": []string{"0"},
		"compact":    []string{"1"}, //compact=1 asks for the 6 bytes per peer form
		"left":       []string{strconv.FormatInt(t.Length, 10)},
	}
	base.RawQuery = params.Encode()
	return base.String(), nil
}

//trackers answer in one of two shapes, the compact binary string or an old list of dicts

func parsePeers(raw any) ([]peer.Peer, error) {
	switch v := raw.(type) {
	case string:
		return peer.Unmarshal([]byte(v))

	case []any:
		peers := make([]peer.Peer, 0, len(v))
		for _, entry := range v {
			pd, ok := entry.(map[string]any)
			if !ok {
				continue
			}
			ipStr, _ := pd["ip"].(string)
			portNum, _ := pd["port"].(int64)
			if ipStr == "" || portNum <= 0 || portNum > 65535 {
				continue
			}
			ip := resolveIP(ipStr)
			if ip == nil {
				continue //can't reach a peer I can't resolve, so I skip it rather than fail everything
			}
			peers = append(peers, peer.Peer{IP: ip, Port: uint16(portNum)})
		}
		return peers, nil

	case nil:
		return nil, fmt.Errorf("tracker response has no peers field")

	default:
		return nil, fmt.Errorf("peers field is %T, want string or list", raw)
	}
}

//the old peer format allows a hostname instead of an ip, so I fall back to DNS

func resolveIP(s string) net.IP {
	if ip := net.ParseIP(s); ip != nil {
		return ip
	}
	ips, err := net.LookupIP(s)
	if err != nil || len(ips) == 0 {
		return nil
	}
	return ips[0]
}
