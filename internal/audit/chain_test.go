// SPDX-License-Identifier: GPL-2.0-or-later

package audit

import (
	"bytes"
	"strings"
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

func TestExportAndReadBackStillVerifies(t *testing.T) {
	c := build(6)
	var buf bytes.Buffer
	if err := WriteLines(&buf, c); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(buf.String(), "\n"); n != 6 {
		t.Fatalf("want one line per entry, got %d lines", n)
	}
	back, err := ReadLines(&buf)
	if err != nil || len(back) != 6 {
		t.Fatalf("%d entries, %v", len(back), err)
	}
	if i := Verify(back); i != -1 {
		t.Fatalf("an exported log must verify offline, bad at %d", i)
	}
	// sub-second timestamps survive the round trip: the hash covers them
	e := Seal(Entry{Seq: 1, Time: time.Unix(5, 123456789), Actor: "a", Action: "b"}, GenesisHash)
	var one bytes.Buffer
	_ = WriteLines(&one, []Entry{e})
	got, _ := ReadLines(&one)
	if Verify(got) != -1 {
		t.Fatal("nanoseconds were lost in the export")
	}
}

func TestEditingAnExportedLineIsCaught(t *testing.T) {
	var buf bytes.Buffer
	_ = WriteLines(&buf, build(4))
	edited := strings.Replace(buf.String(), `"detail":"d"`, `"detail":"x"`, 1)
	back, err := ReadLines(strings.NewReader(edited))
	if err != nil {
		t.Fatal(err)
	}
	if i := Verify(back); i != 0 {
		t.Fatalf("want the edited first line flagged, got %d", i)
	}
	if _, err := ReadLines(strings.NewReader("not json\n")); err == nil {
		t.Fatal("garbage was read as a log")
	}
}

// The chain alone cannot see entries cut off the end: the head (count and last hash) can. That is
// why the admin app shows the head to be kept somewhere else.
func TestHeadSeesATruncatedLogThatTheChainAloneCannot(t *testing.T) {
	c := build(5)
	before := HeadOf(c)
	cut := c[:3]
	if Verify(cut) != -1 {
		t.Fatal("a truncated chain is still internally consistent")
	}
	after := HeadOf(cut)
	if after == before || after.Count != 3 || before.Count != 5 || before.Seq != 5 {
		t.Fatalf("head before %+v after %+v", before, after)
	}
	if h := HeadOf(nil); h.Hash != GenesisHash || h.Count != 0 {
		t.Fatalf("empty head %+v", h)
	}
}
