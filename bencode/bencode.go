package bencode

import (
	"bufio"
	"bytes"
	"crypto/sha1"
	"fmt"
	"io"
	"sort"
	"strconv"
)

func DecodeInt(r *bufio.Reader) (int64, error) {
	_, err := r.ReadByte()
	if err != nil {
		return 0, err
	}

	s, err := r.ReadString('e')
	if err != nil {
		return 0, err
	}
	//readbyte takes away my starting 'i'
	//ReadString reads through the e but keeps it in the string, so I slice it off.)
	s = s[:len(s)-1]
	return strconv.ParseInt(s, 10, 64)
}

//string is encoded as <length>:<string>
//for example, 4:spam

func DecodeString(r *bufio.Reader) (string, error) {
	//can't use ReadByte here because it read the number and move the cursor forward, so no way to know the length of the String
	// _, err := r.ReadByte()
	// if err != nil {
	// 	return "", err
	// }
	//storing the length of the string in a variable, so we can read that many bytes from the reader
	lenStr, err := r.ReadString(':') // ":" includes the colon, so now we have string as "4:spam"
	if err != nil {
		return "", err
	}

	n, err := strconv.Atoi(lenStr[:len(lenStr)-1]) //slice off the colon
	if err != nil {
		return "", err
	}

	buf := make([]byte, n)
	_, err = io.ReadFull(r, buf)
	if err != nil {
		return "", err
	}

	return string(buf), nil
}

//now i'll create a function that will take a reader and call the function based on the first byte

func Decode(r *bufio.Reader) (any, error) {
	b, err := r.Peek(1)
	if err != nil {
		return nil, err
	}
	//switch is used when we have multiple conditions to check, instead of multiple if else statements.
	switch {
	case b[0] == 'i':
		return DecodeInt(r)
	case b[0] >= '0' && b[0] <= '9':
		return DecodeString(r)
	case b[0] == 'l':
		return DecodeList(r) //this is to handle the case where the next element is a list, so we call DecodeList to decode it
		//like l4:spam4:eggse
	case b[0] == 'd':
		return DecodeDict(r) //this is to handle when the next element is a dict
	default:
		return nil, fmt.Errorf("unexpected byte: %q", b[0])
	}
}

func DecodeList(r *bufio.Reader) ([]any, error) {
	//input example
	//l4:spam4:eggse
	_, err := r.ReadByte() //consuming the 'l' character
	if err != nil {
		return nil, err
	}

	result := []any{}

	for {
		b, err := r.Peek(1) //doing this to check if the next byte is 'e' or not, if it is then we break the loop
		if err != nil {
			return nil, err
		}
		if b[0] == 'e' {
			r.ReadByte() //consuming the 'e' character
			return result, nil
		}

		val, err := Decode(r)
		if err != nil {
			return nil, err
		}
		result = append(result, val)
	}
}

//decoding a dictionary is a bit more complicated
//I've to decode the key first that's a string
//then I've to decode the value which can be any type
//so I'll use the Decode function to decode the value

func DecodeDict(r *bufio.Reader) (map[string]any, error) {
	_, err := r.ReadByte() //consuming the 'd' character
	if err != nil {
		return nil, err
	}

	result := make(map[string]any)

	for {
		b, err := r.Peek(1) //doing this to check if the next byte is 'e' or not, if yes I break out of the loop
		if err != nil {
			return nil, err
		}
		if b[0] == 'e' {
			r.ReadByte() //consuming the 'e' character
			return result, nil
		}

		//now I decode the remaining key which can be a string
		//example: d3:cow3:moo4:spam4:eggse

		key, err := DecodeString(r)
		if err != nil {
			return nil, err
		}

		val, err := Decode(r)
		if err != nil {
			return nil, err
		}

		result[key] = val
	}

}

//input example
//d3:cow3:moo4:spam4:eggse

//the info hash is a SHA-1 over the bencoded form of the info dictionary
//now we have the info block in memory as a map[string]any, we need to bencode it back to get the info hash

func Encode(w io.Writer, v any) error {
	switch val := v.(type) { //type switch to check type of val
	case int64:
		_, err := fmt.Fprintf(w, "i%de", val)
		return err
	case int:
		_, err := fmt.Fprintf(w, "i%de", val)
		return err
	case string:
		//same <length>:<string> shape I decode, so "spam" goes back out as 4:spam
		_, err := fmt.Fprintf(w, "%d:%s", len(val), val)
		return err
	case []byte:
		//"pieces" is raw SHA-1 bytes, not text, but bencode encodes it exactly like a string
		if _, err := fmt.Fprintf(w, "%d:", len(val)); err != nil {
			return err
		}
		_, err := w.Write(val)
		return err
	case []any:
		//l<items>e, so I write the 'l', recurse into every item, then close with 'e'
		if _, err := io.WriteString(w, "l"); err != nil {
			return err
		}
		for _, item := range val {
			if err := Encode(w, item); err != nil {
				return err
			}
		}
		_, err := io.WriteString(w, "e")
		return err
	case map[string]any:
		//d<key><value>...e, but Go maps have no order and the info hash does,
		//so I have to sort the keys or every client computes a different hash than me
		if _, err := io.WriteString(w, "d"); err != nil {
			return err
		}
		keys := make([]string, 0, len(val))
		for k := range val {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if err := Encode(w, k); err != nil { //the key is just a bencoded string too
				return err
			}
			if err := Encode(w, val[k]); err != nil {
				return err
			}
		}
		_, err := io.WriteString(w, "e")
		return err
	default:
		return fmt.Errorf("unsupported type: %T", val)
	}
}

//this is the 20 byte id I send to the tracker and to every peer in the handshake
//I bencode the info dict back and SHA-1 that, sorted keys is what makes it match everyone else

func InfoHash(torrent map[string]any) ([20]byte, error) {
	info, ok := torrent["info"]
	if !ok {
		return [20]byte{}, fmt.Errorf("torrent has no \"info\" dictionary")
	}

	var buf bytes.Buffer
	if err := Encode(&buf, info); err != nil {
		return [20]byte{}, fmt.Errorf("encoding info dict: %w", err)
	}

	return sha1.Sum(buf.Bytes()), nil
}
