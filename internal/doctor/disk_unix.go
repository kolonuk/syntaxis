// SPDX-License-Identifier: GPL-2.0-or-later

//go:build unix

package doctor

import "syscall"

func diskFreePct(path string) float64 {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil || st.Blocks == 0 {
		return 0
	}
	return 100 * float64(st.Bavail) / float64(st.Blocks)
}
