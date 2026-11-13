// SPDX-License-Identifier: GPL-2.0-or-later

package audit

import (
	"testing"
	"time"
)

func build(n int) []Entry {
	var out []Entry
	prev := GenesisHash
	for i := 0; i < n; i++ {
		e := Seal(Entry{Seq: int64(i + 1), Time: time.Unix(int64(1000+i), 0), Actor: "admin:1", Action: "user.create", Target: "user:2", Detail: "d"}, prev)
		out = append(out, e)
		prev = e.Hash
	}
	return out
}

func TestIntact(t *testing.T) {
	if i := Verify(build(5)); i != -1 {
		t.Fatalf("intact chain flagged at %d", i)
	}
	if Verify(nil) != -1 {
		t.Fatal("empty chain must verify")
	}
}

func TestTamperContent(t *testing.T) {
	c := build(5)
	c[2].Detail = "changed"
	if i := Verify(c); i != 2 {
		t.Fatalf("want 2 got %d", i)
	}
}

func TestDeleteMiddle(t *testing.T) {
	c := build(5)
	c = append(c[:2], c[3:]...)
	if i := Verify(c); i != 2 {
		t.Fatalf("want 2 got %d", i)
	}
}

func TestRewriteHashStillBreaksNext(t *testing.T) {
	c := build(4)
	c[1].Detail = "evil"
	c[1].Hash = ComputeHash(c[1])
	if i := Verify(c); i != 2 {
		t.Fatalf("want 2 got %d", i)
	}
}

func TestTruncateTailUndetectableByChainAlone(t *testing.T) {
	// Documented limitation: dropping trailing entries keeps the chain valid.
	// Mitigation is exporting/anchoring the head hash externally.
	if Verify(build(5)[:3]) != -1 {
		t.Fatal("prefix should verify")
	}
}
