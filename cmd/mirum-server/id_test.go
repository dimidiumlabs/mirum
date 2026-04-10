// Copyright (c) 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"testing"

	"github.com/google/uuid"
)

func TestIDRoundTrip(t *testing.T) {
	for range 50 {
		id := NewID[UserKind]()
		got, err := ParseID[UserKind](id.String())
		if err != nil {
			t.Fatalf("parse prefixed: %v", err)
		}
		if got != id {
			t.Fatalf("prefixed: got %v, want %v", got, id)
		}
		got, err = ParseID[UserKind](id.Bare())
		if err != nil {
			t.Fatalf("parse bare: %v", err)
		}
		if got != id {
			t.Fatalf("bare: got %v, want %v", got, id)
		}
	}
}

func TestIDRoundTripCanonicalUUID(t *testing.T) {
	id := NewID[OrgKind]()
	got, err := ParseID[OrgKind](id.UUID().String())
	if err != nil {
		t.Fatalf("parse canonical: %v", err)
	}
	if got != id {
		t.Fatalf("canonical: got %v, want %v", got, id)
	}
}

func TestIDCrossPrefixRejected(t *testing.T) {
	id := NewID[UserKind]()
	_, err := ParseID[OrgKind](id.String())
	if err == nil {
		t.Fatal("expected error parsing usr_ as org")
	}
}

func TestBase58EdgeCases(t *testing.T) {
	cases := [][16]byte{
		{}, // all zeros
		{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1}, // minimal
		{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff,
			0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}, // max
	}
	for _, raw := range cases {
		enc := encodeBase58(uuid.UUID(raw))
		dec, err := decodeBase58(enc)
		if err != nil {
			t.Fatalf("decode(%q): %v", enc, err)
		}
		if dec != uuid.UUID(raw) {
			t.Fatalf("roundtrip: got %x, want %x", dec, raw)
		}
	}
}

func TestIDFromBytesRejectsV4(t *testing.T) {
	v4 := uuid.Must(uuid.NewRandom()) // v4
	_, err := IDFromBytes[UserKind](v4[:])
	if err == nil {
		t.Fatal("expected v4 rejection")
	}
}
