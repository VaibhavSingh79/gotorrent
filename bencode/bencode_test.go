package bencode

import (
	"bufio"
	"bytes"
	"crypto/sha1"
	"reflect"
	"strings"
	"testing"
)

func decodeString(t *testing.T, s string) any {
	t.Helper()
	v, err := Decode(bufio.NewReader(strings.NewReader(s)))
	if err != nil {
		t.Fatalf("Decode(%q): %v", s, err)
	}
	return v
}

func TestDecode(t *testing.T) {
	cases := []struct {
		in   string
		want any
	}{
		{"i42e", int64(42)},
		{"i-13e", int64(-13)},
		{"i0e", int64(0)},
		{"4:spam", "spam"},
		{"0:", ""},
		{"l4:spam4:eggse", []any{"spam", "eggs"}},
		{"le", []any{}},
		{"d3:cow3:moo4:spam4:eggse", map[string]any{"cow": "moo", "spam": "eggs"}},
		{"de", map[string]any{}},
		{"d4:spaml1:a1:bee", map[string]any{"spam": []any{"a", "b"}}},
	}
	for _, tc := range cases {
		if got := decodeString(t, tc.in); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("Decode(%q) = %#v, want %#v", tc.in, got, tc.want)
		}
	}
}

func TestEncode(t *testing.T) {
	cases := []struct {
		in   any
		want string
	}{
		{int64(42), "i42e"},
		{int64(-13), "i-13e"},
		{42, "i42e"},
		{"spam", "4:spam"},
		{"", "0:"},
		{[]byte{0xDE, 0xAD}, "2:\xde\xad"},
		{[]any{"spam", "eggs"}, "l4:spam4:eggse"},
		{[]any{}, "le"},
		{map[string]any{"cow": "moo", "spam": "eggs"}, "d3:cow3:moo4:spam4:eggse"},
		{map[string]any{}, "de"},
		//Keys must come out sorted regardless of insertion order; this is
		//what makes the info hash reproducible.
		{map[string]any{"zebra": int64(1), "apple": int64(2), "mango": int64(3)},
			"d5:applei2e5:mangoi3e5:zebrai1ee"},
		{map[string]any{"nested": []any{int64(1), "two"}}, "d6:nestedli1e3:twoee"},
	}
	for _, tc := range cases {
		var buf bytes.Buffer
		if err := Encode(&buf, tc.in); err != nil {
			t.Errorf("Encode(%#v): %v", tc.in, err)
			continue
		}
		if got := buf.String(); got != tc.want {
			t.Errorf("Encode(%#v) = %q, want %q", tc.in, got, tc.want)
		}
	}

	//Unsupported types must error rather than silently emit nothing.
	if err := Encode(&bytes.Buffer{}, 3.14); err == nil {
		t.Error("expected an error encoding a float")
	}
}

func TestRoundTrip(t *testing.T) {
	//A realistic torrent-shaped structure.
	original := "d8:announce18:http://tracker/ann4:infod6:lengthi1024e4:name8:test.iso12:piece lengthi256e6:pieces20:aaaaaaaaaaaaaaaaaaaaee"

	decoded := decodeString(t, original)
	var buf bytes.Buffer
	if err := Encode(&buf, decoded); err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if got := buf.String(); got != original {
		t.Errorf("round trip changed the encoding:\n got %q\nwant %q", got, original)
	}
}

func TestInfoHash(t *testing.T) {
	raw := "d8:announce18:http://tracker/ann4:infod6:lengthi1024e4:name8:test.iso12:piece lengthi256e6:pieces20:aaaaaaaaaaaaaaaaaaaaee"
	dict := decodeString(t, raw).(map[string]any)

	got, err := InfoHash(dict)
	if err != nil {
		t.Fatalf("InfoHash: %v", err)
	}

	//The info hash must equal the SHA-1 of the raw info dictionary bytes as
	//they appear in the file.
	start := strings.Index(raw, "4:infod") + len("4:info")
	want := sha1.Sum([]byte(raw[start : len(raw)-1]))
	if got != want {
		t.Errorf("InfoHash = %x, want %x", got, want)
	}

	if _, err := InfoHash(map[string]any{}); err == nil {
		t.Error("expected an error when the info dictionary is missing")
	}
}

func TestDecodeErrors(t *testing.T) {
	bad := []string{
		"",        // empty
		"x",       // unknown type byte
		"i12",     // unterminated integer
		"ihelloe", // non-numeric integer
		"4:ab",    // string shorter than its length prefix
		"l4:spam", // unterminated list
		"d3:cow",  // dict with a key but no value
	}
	for _, in := range bad {
		if _, err := Decode(bufio.NewReader(strings.NewReader(in))); err == nil {
			t.Errorf("Decode(%q) succeeded, want an error", in)
		}
	}
}
