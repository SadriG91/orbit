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
	// The return values are deliberately not checked, and this is the contract
	// rather than an oversight. crypto/rand.Read "never returns an error, and
	// always fills b entirely" — it calls io.ReadFull on Reader and crashes the
	// program irrecoverably if that fails. Both failure modes a check would
	// guard against, an error and a short read, are excluded by the API, so the
	// check would be unreachable code standing between a reader and the reason
	// it is unreachable. Crashing is also the outcome we would want: a
	// predictable session id is worse than no session id.
	rand.Read(b[:])             //nolint:errcheck // documented never to fail; see above
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
