package dispatch

import (
	"crypto/rand"
	"encoding/hex"
)

// uuid4 returns a random UUID in the canonical 8-4-4-4-12 form.
//
// Hand-rolled rather than a dependency: this is the only place orbit needs a
// UUID, and it needs one only because claude and copilot validate the shape of
// --session-id and reject anything that is not a version 4 UUID. Twenty lines
// against a module in the go.sum of a binary whose selling point is that it has
// almost nothing in it.
func uuid4() string {
	var b [16]byte
	// crypto/rand.Read is documented never to return an error since Go 1.24;
	// it panics internally if the system source fails, which is the right
	// outcome here anyway — a predictable session id is worse than a crash.
	rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	var out [36]byte
	hex.Encode(out[0:8], b[0:4])
	out[8] = '-'
	hex.Encode(out[9:13], b[4:6])
	out[13] = '-'
	hex.Encode(out[14:18], b[6:8])
	out[18] = '-'
	hex.Encode(out[19:23], b[8:10])
	out[23] = '-'
	hex.Encode(out[24:36], b[10:16])
	return string(out[:])
}
