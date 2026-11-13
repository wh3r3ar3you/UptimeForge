package update

import "crypto/ed25519"

// ReleaseKeys are the public keys whose signature on checksums.txt an agent
// accepts. Several keys allow rotation: a new key ships in a release signed
// by the old one before the old one is retired.
//
// The private half lives only in the UF_RELEASE_KEY repository secret (and
// the maintainer's offline backup). To add a key, append
//
//	mustPublicKey("<base64 public key printed by `go run ./tools/sign keygen` on stderr>"),
var ReleaseKeys = []ed25519.PublicKey{
	mustPublicKey("MQfu9mQQM0f5Z6pFl1cR+bv+V9H3tqv2g8t8yPV5e3s="), // 2026-11 release key
}

// mustPublicKey decodes a key written into ReleaseKeys; a typo there is a
// build mistake, so it panics at start.
func mustPublicKey(s string) ed25519.PublicKey {
	keys, err := ParsePublicKeys(s)
	if err != nil || len(keys) != 1 {
		panic("update: bad release key " + s)
	}
	return keys[0]
}
