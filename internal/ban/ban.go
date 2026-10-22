// Package ban holds Banano basics: address validation, block hashes and amounts.
package ban

import (
	"bytes"
	"errors"
	"math/big"
	"strings"

	"golang.org/x/crypto/blake2b"
)

const alphabet = "13456789abcdefghijkmnopqrstuwxyz"

// RawPerBAN is 10^29: one BAN in raw units.
var RawPerBAN = new(big.Int).Exp(big.NewInt(10), big.NewInt(29), nil)

var decode [256]int8

func init() {
	for i := range decode {
		decode[i] = -1
	}
	for i := 0; i < len(alphabet); i++ {
		decode[alphabet[i]] = int8(i)
	}
}

var (
	ErrNotAddress = errors.New("not a Banano address")
	ErrChecksum   = errors.New("address checksum does not match")
)

// ParseAddress validates a ban_ address (checksum included) and returns its public key.
// A typo anywhere in the address makes the checksum fail, so ErrChecksum means
// "looks like an address but is mistyped".
func ParseAddress(s string) ([]byte, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if !strings.HasPrefix(s, "ban_") || len(s) != 64 {
		return nil, ErrNotAddress
	}
	body := s[4:]
	if body[0] != '1' && body[0] != '3' {
		return nil, ErrNotAddress
	}
	n := new(big.Int)
	for i := 0; i < len(body); i++ {
		v := decode[body[i]]
		if v < 0 {
			return nil, ErrNotAddress
		}
		n.Lsh(n, 5)
		n.Or(n, big.NewInt(int64(v)))
	}
	// 60 chars * 5 bits = 300 bits: 4 zero padding bits, 256 key bits, 40 checksum bits.
	if n.BitLen() > 296 {
		return nil, ErrNotAddress
	}
	sum := new(big.Int).And(n, new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 40), big.NewInt(1))).FillBytes(make([]byte, 5))
	key := new(big.Int).Rsh(n, 40).FillBytes(make([]byte, 32))
	if !bytes.Equal(reverse(checksumOf(key)), sum) {
		return nil, ErrChecksum
	}
	return key, nil
}

// LooksLikeAddress is true for strings shaped like an address, valid or not.
func LooksLikeAddress(s string) bool {
	s = strings.ToLower(strings.TrimSpace(s))
	return strings.HasPrefix(s, "ban_") && len(s) >= 60 && len(s) <= 66
}

// Address encodes a 32-byte public key as a ban_ address.
func Address(key []byte) string {
	n := new(big.Int).SetBytes(key)
	n.Lsh(n, 40)
	n.Or(n, new(big.Int).SetBytes(reverse(checksumOf(key))))
	out := make([]byte, 60)
	mask := big.NewInt(31)
	t := new(big.Int)
	for i := 59; i >= 0; i-- {
		out[i] = alphabet[t.And(n, mask).Int64()]
		n.Rsh(n, 5)
	}
	return "ban_" + string(out)
}

func checksumOf(key []byte) []byte {
	h, _ := blake2b.New(5, nil)
	h.Write(key)
	return h.Sum(nil)
}

func reverse(b []byte) []byte {
	r := make([]byte, len(b))
	for i := range b {
		r[len(b)-1-i] = b[i]
	}
	return r
}

// IsHash is true for a 64-character hex block hash.
func IsHash(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}

// Short returns ban_1jung1eb…uzpca style addresses (first 12, last 6).
func Short(addr string) string {
	if len(addr) < 20 {
		return addr
	}
	return addr[:12] + "…" + addr[len(addr)-6:]
}

// ShortHash returns the first 8 characters and an ellipsis.
func ShortHash(h string) string {
	if len(h) <= 8 {
		return h
	}
	return h[:8] + "…"
}
