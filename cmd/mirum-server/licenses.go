// Copyright (c) 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"encoding/json"
	"net/http"
	"strings"

	mirum "dimidiumlabs/mirum"
)

// primarySPDX is the SPDX identifier of mirum itself.
const primarySPDX = "AGPL-3.0-or-later"

// licensesPageData mirrors tools/licensegen's Manifest with a primary
// license header prepended. The frontend consumes it verbatim.
type licensesPageData struct {
	Primary struct {
		Name string `json:"name"`
		SPDX string `json:"spdx"`
		Text string `json:"text"`
	} `json:"primary"`
	Manifest json.RawMessage `json:"manifest"`
}

var licensesData = mustLoadLicenses()

func mustLoadLicenses() *licensesPageData {
	if !json.Valid(mirum.Licenses) {
		panic("embedded licenses.json is not valid JSON")
	}
	d := &licensesPageData{Manifest: mirum.Licenses}
	d.Primary.Name = "mirum"
	d.Primary.SPDX = primarySPDX
	d.Primary.Text = strings.TrimSpace(mirum.License) + "\n"
	return d
}

// licenses serves /about/licenses. Public: no auth, no CSRF.
func (h *webHandler) licenses(w http.ResponseWriter, _ *http.Request) {
	h.assets.renderPage(w, "licenses", http.StatusOK, licensesData)
}
