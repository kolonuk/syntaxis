// SPDX-License-Identifier: GPL-2.0-or-later

package agent

import (
	"fmt"
	"strings"
)

// Editing helpers shared by patch_file and apply_patch.
//
// Weak local models often copy a block with the wrong indentation, tabs for spaces or trailing
// blanks. An exact match is tried first; if that finds nothing, a whole-line match that ignores
// differences in whitespace is tried, and the replacement is re-indented to the file. A match that is
// not unique is never guessed at, and a miss reports the nearest block so the model can correct it.

// Edit is one replacement.
type Edit struct {
	Old string `json:"old"`
	New string `json:"new"`
}

// editError explains why a replacement could not be made.
type editError struct{ msg string }

func (e *editError) Error() string { return e.msg }

func normLine(s string) string { return strings.Join(strings.Fields(s), " ") }

func splitLines(s string) []string {
	s = strings.TrimSuffix(s, "\n")
	if s == "" {
		return []string{""}
	}
	return strings.Split(s, "\n")
}

// lineOffsets returns the byte offset of the start of every line of content.
func lineOffsets(content string) []int {
	offs := []int{0}
	for i := 0; i < len(content); i++ {
		if content[i] == '\n' {
			offs = append(offs, i+1)
		}
	}
	return offs
}

func leadingSpace(s string) string { return s[:len(s)-len(strings.TrimLeft(s, " \t"))] }

// reindent rewrites the indentation of newText into the file's own style. The matched lines tell it
// how: each indentation the model used in the old block is paired with the one the file has on the
// same line. A new line with an indentation the model did not use before is placed relative to the
// longest of those it did.
func reindent(newText string, oldLines, fileLines []string) string {
	pair := map[string]string{}
	for i := range oldLines {
		if strings.TrimSpace(oldLines[i]) == "" {
			continue
		}
		o := leadingSpace(oldLines[i])
		if _, seen := pair[o]; !seen {
			pair[o] = leadingSpace(fileLines[i])
		}
	}
	same := true
	for o, f := range pair {
		if o != f {
			same = false
		}
	}
	if same {
		return newText
	}
	lines := strings.Split(newText, "\n")
	for i, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		ind, body := leadingSpace(l), strings.TrimLeft(l, " \t")
		if f, ok := pair[ind]; ok {
			lines[i] = f + body
			continue
		}
		bestO, bestF := "", ""
		for o, f := range pair {
			if strings.HasPrefix(ind, o) && len(o) >= len(bestO) {
				bestO, bestF = o, f
			}
		}
		if _, hasEmpty := pair[""]; bestO == "" && !hasEmpty {
			lines[i] = l
			continue
		}
		lines[i] = bestF + strings.TrimPrefix(ind, bestO) + body
	}
	return strings.Join(lines, "\n")
}

// replaceBlock replaces one block of content. The second result says how it was matched.
func replaceBlock(content string, ed Edit) (string, string, error) {
	if ed.Old == "" {
		return "", "", &editError{"`old` is empty"}
	}
	if n := strings.Count(content, ed.Old); n == 1 {
		return strings.Replace(content, ed.Old, ed.New, 1), "exact", nil
	} else if n > 1 {
		return "", "", &editError{fmt.Sprintf("`old` text occurs %d times; include more surrounding lines so it is unique", n)}
	}

	oldLines := splitLines(ed.Old)
	contentLines := strings.Split(content, "\n")
	if strings.HasSuffix(content, "\n") {
		contentLines = contentLines[:len(contentLines)-1]
	}
	norm := make([]string, len(contentLines))
	for i, l := range contentLines {
		norm[i] = normLine(l)
	}
	want := make([]string, len(oldLines))
	for i, l := range oldLines {
		want[i] = normLine(l)
	}
	var hits []int
	for i := 0; i+len(want) <= len(norm); i++ {
		ok := true
		for j := range want {
			if norm[i+j] != want[j] {
				ok = false
				break
			}
		}
		if ok {
			hits = append(hits, i)
		}
	}
	switch {
	case len(hits) > 1:
		return "", "", &editError{fmt.Sprintf("`old` matches %d places once spaces and indentation are ignored (first at line %d); include more surrounding lines so it is unique", len(hits), hits[0]+1)}
	case len(hits) == 1:
		i := hits[0]
		offs := lineOffsets(content)
		start := offs[i]
		var end int
		if i+len(want) < len(offs) {
			end = offs[i+len(want)] - 1 // before the newline that ends the last line of the block
		} else {
			end = len(strings.TrimSuffix(content, "\n"))
		}
		repl := reindent(strings.TrimSuffix(ed.New, "\n"), oldLines, contentLines[i:i+len(want)])
		return content[:start] + repl + content[end:], "whitespace-tolerant", nil
	}
	return "", "", &editError{notFoundMessage(contentLines, oldLines)}
}

func bigrams(s string) map[string]int {
	m := map[string]int{}
	r := []rune(s)
	for i := 0; i+1 < len(r); i++ {
		m[string(r[i:i+2])]++
	}
	return m
}

// similarity is the Dice coefficient of the character pairs of two lines, 0 to 1.
func similarity(a, b string) float64 {
	a, b = normLine(a), normLine(b)
	if a == b {
		return 1
	}
	if len(a) < 2 || len(b) < 2 {
		return 0
	}
	ba, bb := bigrams(a), bigrams(b)
	common, total := 0, 0
	for k, n := range ba {
		total += n
		if m := bb[k]; m < n {
			common += m
		} else {
			common += n
		}
	}
	for _, n := range bb {
		total += n
	}
	return 2 * float64(common) / float64(total)
}

// notFoundMessage names the block of the file that is closest to what the model asked for.
func notFoundMessage(contentLines, oldLines []string) string {
	best, bestScore := -1, 0.0
	for i := 0; i+len(oldLines) <= len(contentLines); i++ {
		sum := 0.0
		for j, ol := range oldLines {
			sum += similarity(contentLines[i+j], ol)
		}
		if s := sum / float64(len(oldLines)); s > bestScore {
			best, bestScore = i, s
		}
	}
	if best < 0 || bestScore < 0.45 {
		return "`old` text was not found; read the file again and copy the exact text"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "`old` text was not found. The closest text is at line %d (%d%% alike):\n", best+1, int(bestScore*100))
	for j := 0; j < len(oldLines) && j < 12; j++ {
		fmt.Fprintf(&b, "%d\t%s\n", best+j+1, contentLines[best+j])
	}
	if len(oldLines) > 12 {
		b.WriteString("...\n")
	}
	b.WriteString("Copy the lines exactly as they are in the file, or read it again.")
	return b.String()
}

// applyEdits applies the edits one after another to content. It returns the new content or an error
// naming the first edit that failed; nothing is applied unless all of them are.
func applyEdits(content string, edits []Edit) (string, []string, error) {
	var how []string
	for i, ed := range edits {
		out, kind, err := replaceBlock(content, ed)
		if err != nil {
			if len(edits) == 1 {
				return "", nil, err
			}
			return "", nil, fmt.Errorf("edit %d of %d: %w. No edit was applied", i+1, len(edits), err)
		}
		content = out
		how = append(how, kind)
	}
	return content, how, nil
}

// parseUnifiedDiff reads the hunks of a unified diff for one file. Line numbers in the @@ headers
// are ignored, because models count them badly: each hunk is found in the file by its content. It
// returns the file named in the header (b/ prefix removed), if any.
func parseUnifiedDiff(diff string) (path string, edits []Edit, err error) {
	var oldB, newB []string
	inHunk := false
	flush := func() {
		if inHunk && (len(oldB) > 0 || len(newB) > 0) {
			edits = append(edits, Edit{Old: strings.Join(oldB, "\n"), New: strings.Join(newB, "\n")})
		}
		oldB, newB, inHunk = nil, nil, false
	}
	for _, l := range strings.Split(strings.ReplaceAll(diff, "\r\n", "\n"), "\n") {
		switch {
		case strings.HasPrefix(l, "+++ "):
			if path == "" {
				p := strings.TrimSpace(strings.TrimPrefix(l, "+++ "))
				p = strings.SplitN(p, "\t", 2)[0]
				if p != "/dev/null" {
					path = strings.TrimPrefix(p, "b/")
				}
			}
		case strings.HasPrefix(l, "--- ") && !inHunk, strings.HasPrefix(l, "diff "), strings.HasPrefix(l, "index "):
			continue // file headers (inside a hunk, "--- x" is a removed line that starts with "-- ")
		case strings.HasPrefix(l, "@@"):
			flush()
			inHunk = true
		case !inHunk:
			continue
		case strings.HasPrefix(l, "+"):
			newB = append(newB, l[1:])
		case strings.HasPrefix(l, "-"):
			oldB = append(oldB, l[1:])
		case strings.HasPrefix(l, " "):
			oldB, newB = append(oldB, l[1:]), append(newB, l[1:])
		case l == "":
			// a blank context line whose leading space was lost
			oldB, newB = append(oldB, ""), append(newB, "")
		default: // "\ No newline at end of file" and anything else that is not a diff line
		}
	}
	flush()
	// trailing blank context lines come from the end of the diff text, not from the file
	for i := range edits {
		edits[i].Old = strings.TrimRight(edits[i].Old, "\n")
		edits[i].New = strings.TrimRight(edits[i].New, "\n")
		if strings.TrimSpace(edits[i].Old) == "" {
			return "", nil, fmt.Errorf("hunk %d has no lines to match (a hunk needs at least one context or removed line)", i+1)
		}
	}
	if len(edits) == 0 {
		return "", nil, fmt.Errorf("the diff has no hunks (lines starting with @@)")
	}
	return path, edits, nil
}
