// Package api holds the wire types and the Endpoint contract. It is the only
// surface clients see; engine, store and hub types stay behind it.
package api

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"strings"
	"time"
)

// ID is a ULID: 48 bits of milliseconds then 80 random bits. Commands are
// client-minted, so IDs sort by creation time across machines.
type ID [16]byte

const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

func NewID() ID { return newIDAt(time.Now()) }

func newIDAt(t time.Time) ID {
	var id ID
	ms := uint64(t.UnixMilli())
	binary.BigEndian.PutUint16(id[0:2], uint16(ms>>32))
	binary.BigEndian.PutUint32(id[2:6], uint32(ms))
	if _, err := rand.Read(id[6:]); err != nil {
		panic(err)
	}
	return id
}

func (id ID) IsZero() bool { return id == ID{} }

func (id ID) String() string {
	var b [26]byte
	// 128 bits as 26 base32 digits, the first digit holding the top 3 bits.
	hi := binary.BigEndian.Uint64(id[0:8])
	lo := binary.BigEndian.Uint64(id[8:16])
	for i := 25; i >= 0; i-- {
		b[i] = crockford[lo&31]
		lo = lo>>5 | hi<<59
		hi >>= 5
	}
	return string(b[:])
}

var errBadID = errors.New("api: malformed id")

func ParseID(s string) (ID, error) {
	if len(s) != 26 {
		return ID{}, errBadID
	}
	var hi, lo uint64
	for i := 0; i < 26; i++ {
		var v int
		if c := s[i]; c >= '0' && c <= '9' {
			v = int(c - '0')
		} else {
			v = strings.IndexByte(crockford, c&^0x20)
		}
		if v < 0 || (i == 0 && v > 7) {
			return ID{}, errBadID
		}
		hi = hi<<5 | lo>>59
		lo = lo<<5 | uint64(v)
	}
	var id ID
	binary.BigEndian.PutUint64(id[0:8], hi)
	binary.BigEndian.PutUint64(id[8:16], lo)
	return id, nil
}

func (id ID) MarshalText() ([]byte, error) { return []byte(id.String()), nil }

func (id *ID) UnmarshalText(b []byte) error {
	v, err := ParseID(string(b))
	if err != nil {
		return err
	}
	*id = v
	return nil
}
