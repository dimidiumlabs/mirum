// Copyright (c) 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

package protocol

import (
	"errors"
	"testing"
)

func TestParseVersion(t *testing.T) {
	tests := []struct {
		name                string
		in                  string
		major, minor, patch uint32
		wantErr             bool
	}{
		{"valid", "0.1.0", 0, 1, 0, false},
		{"large", "10.20.30", 10, 20, 30, false},
		{"whitespace", " 1.2.3 ", 1, 2, 3, false},
		{"empty", "", 0, 0, 0, true},
		{"two parts", "1.2", 0, 0, 0, true},
		{"one part", "1", 0, 0, 0, true},
		{"letters", "a.b.c", 0, 0, 0, true},
		{"negative", "-1.0.0", 0, 0, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			major, minor, patch, err := ParseVersion(tt.in)
			if tt.wantErr {
				if !errors.Is(err, ErrInvalidVersion) {
					t.Fatalf("ParseVersion(%q) err = %v, want ErrInvalidVersion", tt.in, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseVersion(%q) unexpected error: %v", tt.in, err)
			}
			if major != tt.major || minor != tt.minor || patch != tt.patch {
				t.Fatalf("ParseVersion(%q) = %d.%d.%d, want %d.%d.%d",
					tt.in, major, minor, patch, tt.major, tt.minor, tt.patch)
			}
		})
	}
}
