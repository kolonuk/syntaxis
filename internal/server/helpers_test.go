// SPDX-License-Identifier: GPL-2.0-or-later

package server

import "github.com/kolonuk/syntaxis/internal/health"

func healthFindingForTest() []health.Finding {
	return []health.Finding{{CheckID: "provider.unreachable", Severity: health.Error, Subject: "", Message: "down", UserMessage: "The model service is unavailable."}}
}
