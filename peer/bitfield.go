package peer

//a peer tells me which pieces it has as one big bit per piece
//piece 0 is the HIGH bit of byte 0, not the low one, which is why I shift by 7-offset

type Bitfield []byte

func (bf Bitfield) HasPiece(index int) bool {
	if index < 0 {
		return false
	}
	byteIndex := index / 8
	offset := index % 8
	if byteIndex >= len(bf) { //a peer can send a short bitfield, that just means it has nothing there
		return false
	}
	return bf[byteIndex]>>(7-offset)&1 != 0
}

func (bf Bitfield) SetPiece(index int) {
	if index < 0 {
		return
	}
	byteIndex := index / 8
	offset := index % 8
	if byteIndex >= len(bf) { //a bogus have message shouldn't panic me, so I just drop it
		return
	}
	bf[byteIndex] |= 1 << (7 - offset)
}
