// SPDX-License-Identifier: GPL-2.0-or-later

// Package audit provides a tamper-evident (hash-chained) audit log.
package audit

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
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

// Head describes the end of a chain: how many entries it holds and the hash of the last one.
// Anyone holding the head hash can later tell whether the log was rewritten behind it, because
// every earlier entry is bound into that single value.
type Head struct {
	Count int    `json:"count"`
	Seq   int64  `json:"seq"`
	Hash  string `json:"hash"`
}

// HeadOf returns the head of a chain; an empty chain's head is the genesis hash.
func HeadOf(entries []Entry) Head {
	if len(entries) == 0 {
		return Head{Hash: GenesisHash}
	}
	last := entries[len(entries)-1]
	return Head{Count: len(entries), Seq: last.Seq, Hash: last.Hash}
}

// WriteLines writes the chain as JSON Lines, one entry per line, the format Syntaxis exports and
// `syntaxis audit verify` reads.
func WriteLines(w io.Writer, entries []Entry) error {
	enc := json.NewEncoder(w)
	for _, e := range entries {
		if err := enc.Encode(e); err != nil {
			return err
		}
	}
	return nil
}

// ReadLines reads a chain written by WriteLines.
func ReadLines(r io.Reader) ([]Entry, error) {
	var out []Entry
	dec := json.NewDecoder(r)
	for dec.More() {
		var e Entry
		if err := dec.Decode(&e); err != nil {
			return nil, fmt.Errorf("entry %d: %w", len(out)+1, err)
		}
		out = append(out, e)
	}
	return out, nil
}
