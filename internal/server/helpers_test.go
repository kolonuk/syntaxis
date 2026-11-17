// SPDX-License-Identifier: GPL-2.0-or-later

package server

import (
	"net/netip"
	"time"

	"github.com/kolonuk/syntaxis/internal/connections"
	"github.com/kolonuk/syntaxis/internal/health"
	"github.com/kolonuk/syntaxis/internal/safehttp"
)

func healthFindingForTest() []health.Finding {
	return []health.Finding{{CheckID: "provider.unreachable", Severity: health.Error, Subject: "", Message: "down", UserMessage: "The model service is unavailable."}}
}

// allowLoopbackConnections lets users' connections reach a test service on loopback, which the
// server refuses by default (see package safehttp).
func allowLoopbackConnections(e *env) {
	e.app.Connections = connections.New(safehttp.NewClient(15*time.Second, netip.MustParsePrefix("127.0.0.0/8")))
}
