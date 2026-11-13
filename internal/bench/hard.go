// SPDX-License-Identifier: GPL-2.0-or-later

package bench

import (
	"context"
	"strings"
	"time"

	"github.com/kolonuk/syntaxis/internal/sandbox"
)

// HardSuite returns realistic mini-projects across common languages and tools.
// Each task works in its own directory and is verified by the checker running
// its own hidden tests, never by trusting the model's claim that tests pass.
func HardSuite() []Task {
	return []Task{goTask(), pythonTask(), nodeTask(), cTask(), pyBugfixTask(), gitTask(), jqTask()}
}

// run executes a shell script in the sandbox and returns trimmed combined output and success.
func run(ctx context.Context, ex ExecFunc, script string) (string, bool) {
	r, err := ex(ctx, sandbox.ExecSpec{Cmd: []string{"sh", "-c", script}, Timeout: 3 * time.Minute})
	return strings.TrimSpace(r.Stdout + r.Stderr), err == nil && r.ExitCode == 0
}

// put writes a hidden checker file.
func put(ctx context.Context, ex ExecFunc, path, content string) {
	_, _ = ex(ctx, sandbox.ExecSpec{Cmd: []string{"sh", "-c", `mkdir -p "$(dirname "$1")" && cat > "$1"`, "_", path}, Stdin: []byte(content), Timeout: 20 * time.Second})
}

func tail(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " | ")
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}

func goTask() Task {
	return Task{ID: "go-project", Category: "go", MaxTurns: 20,
		Prompt: "In /workspace/goslug create a Go module named example.com/slug with slug.go exposing func Slugify(s string) string " +
			"(lowercase, spaces and underscores become single hyphens, drop all characters except a-z, 0-9 and hyphen, trim leading/trailing hyphens) " +
			"and slug_test.go with a table test of at least 4 cases. Run `go test ./...` and make sure it passes. Then reply 'done'.",
		Check: func(ctx context.Context, ex ExecFunc, _ string) (bool, string) {
			put(ctx, ex, "/workspace/goslug/hidden_test.go", `package slug
import "testing"
func TestHidden(t *testing.T) {
	for in, want := range map[string]string{"Hello World": "hello-world", "  a_b  c ": "a-b-c", "Go!Lang": "golang", "--x--": "x", "A  B": "a-b"} {
		if got := Slugify(in); got != want { t.Errorf("Slugify(%q)=%q want %q", in, got, want) }
	}
}`)
			out, ok := run(ctx, ex, "cd /workspace/goslug && go test ./... 2>&1 | tail -6; go test ./... >/dev/null 2>&1")
			return ok, "go test: " + tail(out, 200)
		}}
}

func pythonTask() Task {
	return Task{ID: "python-project", Category: "python", MaxTurns: 20,
		Prompt: "In /workspace/pystats create wordstats.py with func top_words(text, n) returning a list of (word, count) tuples for the n most common words: " +
			"lowercase, words are runs of letters a-z, sorted by count descending then word ascending. Also create test_wordstats.py using unittest with at least 4 cases. " +
			"Run `python3 -m unittest` in that directory and make it pass. Then reply 'done'.",
		Check: func(ctx context.Context, ex ExecFunc, _ string) (bool, string) {
			put(ctx, ex, "/workspace/pystats/hidden_check.py", `import wordstats
assert wordstats.top_words("the cat. The dog! the end, a cat", 2) == [("the", 3), ("cat", 2)], wordstats.top_words("the cat. The dog! the end, a cat", 2)
assert wordstats.top_words("b a b a c", 3) == [("a", 2), ("b", 2), ("c", 1)]
assert wordstats.top_words("", 3) == []
print("ok")`)
			out, ok := run(ctx, ex, "cd /workspace/pystats && python3 hidden_check.py 2>&1 && python3 -m unittest 2>&1 | tail -3")
			return ok, "python: " + tail(out, 200)
		}}
}

func nodeTask() Task {
	return Task{ID: "node-project", Category: "javascript", MaxTurns: 20,
		Prompt: "In /workspace/nodechunk create lib.js (CommonJS) exporting chunk(arr, size) that splits an array into arrays of at most `size` elements (throw an Error if size < 1), " +
			"and lib.test.js using the built-in node:test and node:assert modules with at least 4 cases. Run `node --test` in that directory and make it pass. Then reply 'done'.",
		Check: func(ctx context.Context, ex ExecFunc, _ string) (bool, string) {
			put(ctx, ex, "/workspace/nodechunk/hidden_check.js", `const { chunk } = require("./lib");
const assert = require("assert");
assert.deepStrictEqual(chunk([1,2,3,4,5], 2), [[1,2],[3,4],[5]]);
assert.deepStrictEqual(chunk([], 3), []);
assert.deepStrictEqual(chunk([1,2], 5), [[1,2]]);
assert.throws(() => chunk([1], 0));
console.log("ok");`)
			out, ok := run(ctx, ex, "cd /workspace/nodechunk && node hidden_check.js 2>&1 && node --test 2>&1 | tail -8")
			return ok, "node: " + tail(out, 200)
		}}
}

func cTask() Task {
	return Task{ID: "c-project", Category: "c", MaxTurns: 20,
		Prompt: "In /workspace/ccalc write calc.c and a Makefile. `make` must build an executable named calc. " +
			"`./calc A OP B` takes two integers and one of + - * / (quote * in the shell) and prints the integer result and a newline; " +
			"integer division by zero prints `error` and exits with status 1. Build it and try a few examples. Then reply 'done'.",
		Check: func(ctx context.Context, ex ExecFunc, _ string) (bool, string) {
			script := `cd /workspace/ccalc && make >/dev/null 2>&1 && [ "$(./calc 6 '*' 7)" = 42 ] && [ "$(./calc 9 - 12)" = -3 ] && [ "$(./calc 9 / 2)" = 4 ] && [ "$(./calc 20 + 22)" = 42 ] && { ./calc 1 / 0 >/dev/null 2>&1; [ $? -eq 1 ]; } && echo ok`
			out, ok := run(ctx, ex, script)
			return ok && out == "ok", "c: " + tail(out, 160)
		}}
}

func pyBugfixTask() Task {
	const buggy = `def is_leap(year):
    return year % 4 == 0


def days_in_month(year, month):
    if month == 2:
        return 29 if is_leap(year) else 28
    if month in (4, 6, 9, 11):
        return 30
    return 31
`
	const tests = `import unittest
from dates import days_in_month


class T(unittest.TestCase):
    def test_feb(self):
        self.assertEqual(days_in_month(2023, 2), 28)
        self.assertEqual(days_in_month(2024, 2), 29)

    def test_century(self):
        self.assertEqual(days_in_month(1900, 2), 28)
        self.assertEqual(days_in_month(2000, 2), 29)

    def test_others(self):
        self.assertEqual(days_in_month(2024, 4), 30)
        self.assertEqual(days_in_month(2024, 12), 31)


if __name__ == "__main__":
    unittest.main()
`
	return Task{ID: "python-bugfix", Category: "debugging", MaxTurns: 15,
		Setup: map[string]string{"/workspace/bugfix/dates.py": buggy, "/workspace/bugfix/test_dates.py": tests},
		Prompt: "The tests in /workspace/bugfix fail. Run `python3 -m unittest` there, find the bug in dates.py and fix it. " +
			"Do NOT modify test_dates.py. Rerun the tests until they pass, then reply 'done'.",
		Check: func(ctx context.Context, ex ExecFunc, _ string) (bool, string) {
			t, _ := run(ctx, ex, "cat /workspace/bugfix/test_dates.py")
			if strings.TrimSpace(t) != strings.TrimSpace(tests) {
				return false, "the tests were modified"
			}
			out, ok := run(ctx, ex, "cd /workspace/bugfix && python3 -m unittest 2>&1 | tail -3")
			return ok && strings.Contains(out, "OK"), "unittest: " + tail(out, 160)
		}}
}

func gitTask() Task {
	return Task{ID: "git-workflow", Category: "git", MaxTurns: 20,
		Prompt: "In /workspace/gitrepo: initialise a git repository whose default branch is main, create notes.md containing a line of text and commit it " +
			"(set user.name 'Test' and user.email 'test@example.com' locally in the repo first). Then create a branch named feature, add todo.md with a line of text on it and commit. " +
			"Finally switch back to main. Reply 'done'.",
		Check: func(ctx context.Context, ex ExecFunc, _ string) (bool, string) {
			script := `cd /workspace/gitrepo && [ "$(git rev-parse --abbrev-ref HEAD)" = main ] && git rev-parse --verify -q feature >/dev/null && [ "$(git rev-list --count --all)" -ge 2 ] && git show feature:todo.md >/dev/null && git show main:notes.md >/dev/null && ! git cat-file -e main:todo.md 2>/dev/null && echo ok`
			out, ok := run(ctx, ex, script)
			return ok && out == "ok", "git: " + tail(out, 160)
		}}
}

func jqTask() Task {
	return Task{ID: "jq-data", Category: "shell", MaxTurns: 12,
		Setup: map[string]string{"/workspace/data/people.json": `[{"name":"Zed","age":41},{"name":"Amy","age":30},{"name":"Bob","age":25},{"name":"Cat","age":35}]`},
		Prompt: "Using jq (not by hand), read /workspace/data/people.json and write /workspace/data/adults.csv with a header line `name,age` followed by one `name,age` line " +
			"for each person aged 30 or older, sorted by name ascending. Then reply 'done'.",
		Check: func(ctx context.Context, ex ExecFunc, _ string) (bool, string) {
			out, _ := run(ctx, ex, "cat /workspace/data/adults.csv")
			want := "name,age\nAmy,30\nCat,35\nZed,41"
			return strings.TrimSpace(out) == want, "csv: " + tail(out, 120)
		}}
}
