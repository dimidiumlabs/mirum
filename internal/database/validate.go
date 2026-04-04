// Copyright (c) 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"errors"
	"net/mail"
	"regexp"
	"strings"
)

var slugRe = regexp.MustCompile(`^[a-zA-Z0-9]+(?:-[a-zA-Z0-9]+)*$`)

var validRoles = map[string]bool{
	"owner":  true,
	"admin":  true,
	"member": true,
}

var (
	ErrInvalidEmail = errors.New("database: invalid email")
	ErrInvalidSlug  = errors.New("database: invalid slug")
	ErrInvalidRole  = errors.New("database: invalid role")
)

// ValidateEmail checks that the value is a valid email address.
func ValidateEmail(value string) error {
	if _, err := mail.ParseAddress(value); err != nil {
		return ErrInvalidEmail
	}
	return nil
}

// ValidateSlug checks format and returns the normalized (lowercased) slug.
func ValidateSlug(value string) (string, error) {
	if len(value) < 2 || len(value) > 64 || !slugRe.MatchString(value) {
		return "", ErrInvalidSlug
	}
	return strings.ToLower(value), nil
}

// ValidateRole checks that the value is a valid role string.
func ValidateRole(value string) error {
	if !validRoles[value] {
		return ErrInvalidRole
	}
	return nil
}

// ClampPageSize clamps a page_size to [1, max]. If v is 0 (unset), returns defaultSize.
func ClampPageSize(v int32, defaultSize, max int32) int32 {
	if v <= 0 {
		return defaultSize
	}
	if v > max {
		return max
	}
	return v
}
