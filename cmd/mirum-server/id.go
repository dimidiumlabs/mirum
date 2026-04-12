// Copyright (c) 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"database/sql/driver"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/google/uuid"
)

var (
	ErrBadID       = errors.New("database: bad id")
	ErrBadIDPrefix = errors.New("database: wrong id prefix")
)

type (
	OrgID    = ID[OrgKind]
	UserID   = ID[UserKind]
	WorkerID = ID[WorkerKind]
)

// IDKind is a phantom-type tag that distinguishes otherwise-identical
// 16-byte IDs at the Go type level. Each tag is a zero-sized struct
// that carries only its 3-letter prefix.
type IDKind interface {
	UserKind | OrgKind | WorkerKind
	Prefix() string
}

type (
	UserKind   struct{}
	OrgKind    struct{}
	WorkerKind struct{}
)

func (OrgKind) Prefix() string    { return "org" }
func (UserKind) Prefix() string   { return "usr" }
func (WorkerKind) Prefix() string { return "wrk" }

// ID[K] is a typed UUIDv7. Instantiations with different K are distinct
// types, so passing a UserID where an OrgID is expected is a compile
// error. Cross-kind conversion requires an explicit cast, visible in
// review.
type ID[K IDKind] uuid.UUID

func NewID[K IDKind]() ID[K] {
	return ID[K](uuid.Must(uuid.NewV7()))
}

func IDFromBytes[K IDKind](b []byte) (ID[K], error) {
	var zero ID[K]
	if len(b) != 16 {
		return zero, fmt.Errorf("%w: got %d bytes", ErrBadID, len(b))
	}
	u := uuid.UUID(b)
	if err := validateV7(u); err != nil {
		return zero, err
	}
	return ID[K](u), nil
}

func validateV7(u uuid.UUID) error {
	if v := u.Variant(); v != uuid.RFC4122 {
		return fmt.Errorf("%w: variant %s", ErrBadID, v)
	}
	if v := u.Version(); v != 7 {
		return fmt.Errorf("%w: version %d, want 7", ErrBadID, v)
	}
	return nil
}

func (id ID[K]) UUID() uuid.UUID { return uuid.UUID(id) }
func (id ID[K]) Bytes() []byte   { return id[:] }

func (id ID[K]) IsZero() bool {
	var zero ID[K]
	return id == zero
}

// String returns the prefixed form for logs, JSON, errors and anywhere
// the entity type isn't obvious from context. For URL path segments
// where the route already names the type, use Bare().
func (id ID[K]) String() string {
	var k K
	return k.Prefix() + "_" + encodeBase58(uuid.UUID(id))
}

// Bare returns the base58 form without a type prefix — ≤ 22 chars.
// Use this for URL path segments where the route already identifies
// the entity ("/org/:id"); use String() everywhere else.
func (id ID[K]) Bare() string {
	return encodeBase58(uuid.UUID(id))
}

func (id ID[K]) LogValue() slog.Value {
	return slog.StringValue(id.String())
}

func (id ID[K]) MarshalText() ([]byte, error) {
	return []byte(id.String()), nil
}

func (id *ID[K]) UnmarshalText(b []byte) error {
	parsed, err := ParseID[K](string(b))
	if err != nil {
		return err
	}
	*id = parsed
	return nil
}

func (id *ID[K]) Scan(src any) error {
	var u uuid.UUID
	if err := u.Scan(src); err != nil {
		return err
	}
	*id = ID[K](u)
	return nil
}

func (id ID[K]) Value() (driver.Value, error) {
	return uuid.UUID(id).Value()
}

// ParseID accepts the prefixed base58 ("<prefix>_<b58>"), bare base58
// (≤ 22 chars, as returned by Bare()), or a canonical UUID string.
// Bare forms stay supported so existing CLI flags, manual SQL lookups
// and URL path parameters keep working without a flag day.
func ParseID[K IDKind](s string) (ID[K], error) {
	var k K
	u, err := parseID(k.Prefix(), s)
	return ID[K](u), err
}

func MustParseID[K IDKind](s string) ID[K] {
	id, err := ParseID[K](s)
	if err != nil {
		panic(err)
	}
	return id
}

// ParseAnyID parses a prefixed, bare base58, or canonical UUID string
// into raw 16 bytes. Unlike ParseID[K], it does not require a known
// entity kind — any 3-letter prefix is stripped silently. Intended for
// CLI boundary code that dispatches on proto field names.
func ParseAnyID(s string) ([16]byte, error) {
	if s == "" {
		return [16]byte{}, fmt.Errorf("%w: empty", ErrBadID)
	}
	// Strip any typed prefix.
	if len(s) > 4 && s[3] == '_' {
		s = s[4:]
	}
	if len(s) <= 22 {
		return decodeBase58(s)
	}
	u, err := uuid.Parse(s)
	if err != nil {
		return [16]byte{}, fmt.Errorf("%w: %w", ErrBadID, err)
	}
	return u, nil
}

// FormatAnyID formats raw 16-byte ID as bare base58. Returns base64 for
// non-16-byte inputs as a fallback.
func FormatAnyID(b []byte) string {
	if len(b) == 16 {
		return encodeBase58(uuid.UUID(b))
	}
	return hex.EncodeToString(b)
}

func parseID(prefix, s string) (uuid.UUID, error) {
	if s == "" {
		return uuid.Nil, fmt.Errorf("%w: empty", ErrBadID)
	}

	if rest, ok := strings.CutPrefix(s, prefix+"_"); ok {
		return decodeBase58(rest)
	}

	// Typed prefix with the wrong value — reject so a cross-type paste
	// doesn't silently fall through to the bare path.
	if len(s) > 4 && s[3] == '_' {
		return uuid.Nil, fmt.Errorf("%w: want %q, got %q", ErrBadIDPrefix, prefix, s[:3])
	}

	// Bare form: base58 (≤ 22 chars, from Bare()) or canonical UUID
	// (32–36 chars, from manual SQL / legacy CLI).
	if len(s) <= 22 {
		return decodeBase58(s)
	}

	u, err := uuid.Parse(s)
	if err != nil {
		return uuid.Nil, fmt.Errorf("%w: %w", ErrBadID, err)
	}

	return u, nil
}

// --- base58 (Bitcoin alphabet) ---
//
// 16 bytes fit in ≤ 22 base58 chars: log₅₈(2¹²⁸) ≈ 21.86.
// UUIDv7 values in the post-1970 range always have a non-zero
// leading byte, so encoded length is effectively a constant 21-22.

const b58Alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

var b58Index [256]byte

func init() {
	for i := range b58Index {
		b58Index[i] = 0xff
	}
	for i := 0; i < len(b58Alphabet); i++ {
		b58Index[b58Alphabet[i]] = byte(i)
	}
}

func encodeBase58(src uuid.UUID) string {
	zeros := 0
	for zeros < 16 && src[zeros] == 0 {
		zeros++
	}

	buf := src // array copy; long-division is in place
	start := zeros
	out := make([]byte, 0, 22)

	for start < 16 {
		rem := 0
		for i := start; i < 16; i++ {
			v := rem*256 + int(buf[i])
			buf[i] = byte(v / 58)
			rem = v % 58
		}
		out = append(out, b58Alphabet[rem])
		for start < 16 && buf[start] == 0 {
			start++
		}
	}

	for i := 0; i < zeros; i++ {
		out = append(out, b58Alphabet[0])
	}

	// Reverse into big-endian order.
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}

	return string(out)
}

// decodeBase58 is strict about length — any input that round-trips to a
// value of a different byte length is rejected; we never want a
// 15-byte or 17-byte payload masquerading as a UUID.
func decodeBase58(s string) (uuid.UUID, error) {
	if s == "" {
		return uuid.Nil, fmt.Errorf("%w: empty", ErrBadID)
	}
	zeros := 0
	for zeros < len(s) && s[zeros] == b58Alphabet[0] {
		zeros++
	}

	var out uuid.UUID
	for i := zeros; i < len(s); i++ {
		v := b58Index[s[i]]
		if v == 0xff {
			return uuid.Nil, fmt.Errorf("%w: bad base58 char %q", ErrBadID, s[i])
		}
		carry := int(v)
		for j := 15; j >= 0; j-- {
			acc := int(out[j])*58 + carry
			out[j] = byte(acc)
			carry = acc >> 8
		}
		if carry != 0 {
			return uuid.Nil, fmt.Errorf("%w: overflow", ErrBadID)
		}
	}

	// The '1' prefix count in the string must equal the leading-zero
	// byte count in the result — any mismatch means the input decoded
	// to a different byte length than a UUID.
	actualZeros := 0
	for actualZeros < 16 && out[actualZeros] == 0 {
		actualZeros++
	}
	if actualZeros != zeros {
		return uuid.Nil, fmt.Errorf("%w: length mismatch", ErrBadID)
	}
	return out, nil
}
