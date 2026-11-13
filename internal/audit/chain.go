// SPDX-License-Identifier: GPL-2.0-or-later

// Package audit provides a tamper-evident (hash-chained) audit log.
package audit

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"
)

// Entry is one audit record.
type Entry struct {
	Seq      int64     `json:"seq"`
	Time     time.Time `json:"time"`
	Actor    string    `json:"actor"`
	Action   string    `json:"action"`
	Target   string    `json:"target"`
	Detail   string    `json:"detail"`
	PrevHash string    `json:"prev_hash"`
	Hash     string    `json:"hash"`
}

// GenesisHash is the PrevHash of the first entry.
const GenesisHash = "0000000000000000000000000000000000000000000000000000000000000000"

// ComputeHash returns the hash binding e's content to its predecessor.
func ComputeHash(e Entry) string {
	h := sha256.New()
	fmt.Fprintf(h, "%d\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s",
		e.Seq, e.Time.UTC().Format(time.RFC3339Nano), e.Actor, e.Action, e.Target, e.Detail, e.PrevHash)
	return hex.EncodeToString(h.Sum(nil))
}

// Seal fills PrevHash and Hash given the previous entry's hash.
func Seal(e Entry, prevHash string) Entry {
	e.PrevHash = prevHash
	e.Hash = ComputeHash(e)
	return e
}

// Verify checks a chain from genesis. It returns the index of the first bad
// entry, or -1 when the chain is intact.
func Verify(entries []Entry) int {
	prev := GenesisHash
	for i, e := range entries {
		if e.PrevHash != prev || e.Hash != ComputeHash(e) {
			return i
		}
		prev = e.Hash
	}
	return -1
}
