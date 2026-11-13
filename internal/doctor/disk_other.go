// SPDX-License-Identifier: GPL-2.0-or-later

//go:build !unix

package doctor

func diskFreePct(string) float64 { return 0 }
