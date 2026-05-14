// Package quiz implements the /quiz code-snippet language guessing
// game.
//
// Snippets are baked into the binary; no I/O at runtime. Each snippet
// declares its actual language (the correct answer) and a slate of
// distractors (other plausible languages). The handler picks a random
// snippet, builds a 4-button keyboard with the answer plus three
// distractors in randomised order, and edits the message on first
// correct tap.
package quiz

import (
	"errors"
	"math/rand"
	"sort"
	"time"
)

// Lang is a stable enum so callback_data and persistence stay short
// (one digit per language). Keep the values stable; reordering breaks
// in-flight quiz messages with embedded callback_data after deploys.
type Lang int

const (
	LangPython Lang = iota
	LangGo
	LangJS
	LangRust
	LangClojure
	LangTS
)

// Title returns the human-readable name shown in keyboard buttons and
// in the announcement.
func (l Lang) Title() string {
	switch l {
	case LangPython:
		return "Python"
	case LangGo:
		return "Go"
	case LangJS:
		return "JavaScript"
	case LangRust:
		return "Rust"
	case LangClojure:
		return "Clojure"
	case LangTS:
		return "TypeScript"
	default:
		return "?"
	}
}

// AllLangs lists every language we ever ask about. Used to build
// distractor pools and to validate parsed callback_data.
var AllLangs = []Lang{
	LangPython, LangGo, LangJS, LangRust, LangClojure, LangTS,
}

// Snippet is one code sample plus its correct language. Code is shown
// inside an HTML <pre> block in Telegram so leading/trailing whitespace
// is preserved as-is.
type Snippet struct {
	// Code is the body shown to the user. It is the caller's
	// responsibility to keep this short enough for a comfortable
	// mobile-screen render (under ~25 lines).
	Code string
	// Answer is the correct language. Must be one of AllLangs.
	Answer Lang
}

// snippets is the curated pool. Add new entries at the end so the
// stable indices used by callback_data do not shift for in-flight
// quizzes. The minimum size is 4 so that BuildOptions can always pick
// three distractors.
var snippets = []Snippet{
	{
		Code: `def fib(n):
    a, b = 0, 1
    for _ in range(n):
        a, b = b, a + b
    return a

print(fib(10))`,
		Answer: LangPython,
	},
	{
		Code: `package main

import "fmt"

func main() {
    ch := make(chan int, 3)
    for i := 1; i <= 3; i++ {
        ch <- i
    }
    close(ch)
    for v := range ch {
        fmt.Println(v)
    }
}`,
		Answer: LangGo,
	},
	{
		Code: `const sum = arr =>
  arr.reduce((acc, x) => acc + x, 0);

const result = sum([1, 2, 3, 4, 5]);
console.log(result);`,
		Answer: LangJS,
	},
	{
		Code: `fn main() {
    let v: Vec<i32> = (1..=5).collect();
    let s: i32 = v.iter().sum();
    println!("{}", s);
}`,
		Answer: LangRust,
	},
	{
		Code: `(defn factorial [n]
  (reduce * (range 1 (inc n))))

(println (factorial 5))`,
		Answer: LangClojure,
	},
	{
		Code: `interface User {
  id: number;
  name: string;
}

const user: User = { id: 1, name: "Alice" };
console.log(user.name);`,
		Answer: LangTS,
	},
	{
		Code: `import asyncio

async def fetch(name):
    await asyncio.sleep(0.1)
    return f"hi {name}"

async def main():
    print(await fetch("Alice"))

asyncio.run(main())`,
		Answer: LangPython,
	},
	{
		Code: `func merge[T any](a, b []T) []T {
    out := make([]T, 0, len(a)+len(b))
    out = append(out, a...)
    out = append(out, b...)
    return out
}`,
		Answer: LangGo,
	},
	{
		Code: `pub trait Animal {
    fn name(&self) -> &str;
    fn speak(&self) -> String {
        format!("{} говорит", self.name())
    }
}`,
		Answer: LangRust,
	},
	{
		Code: `(defmacro unless [test & body]
  ` + "`(if (not ~test) (do ~@body)))" + `

(unless false
  (println "this will print"))`,
		Answer: LangClojure,
	},
	{
		Code: `type Result<T, E> = { ok: true; value: T } | { ok: false; error: E };

function divide(a: number, b: number): Result<number, string> {
  if (b === 0) return { ok: false, error: "div by zero" };
  return { ok: true, value: a / b };
}`,
		Answer: LangTS,
	},
	{
		Code: `const promise = new Promise((resolve) => {
  setTimeout(() => resolve(42), 100);
});

promise.then((v) => console.log(v));`,
		Answer: LangJS,
	},
}

// SnippetCount returns the number of available snippets. Mostly used
// in tests to assert "we have a healthy mix" without hardcoding the
// number into multiple tests.
func SnippetCount() int { return len(snippets) }

// LanguageMix returns a sorted list of (language, count) pairs across
// the snippet pool. Useful as a sanity check that the curated set
// actually contains every language we can guess.
func LanguageMix() []LangCount {
	counts := make(map[Lang]int)
	for _, s := range snippets {
		counts[s.Answer]++
	}
	out := make([]LangCount, 0, len(counts))
	for l, c := range counts {
		out = append(out, LangCount{Lang: l, Count: c})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Lang < out[j].Lang })
	return out
}

// LangCount pairs a language with how many snippets ask about it.
type LangCount struct {
	Lang  Lang
	Count int
}

// ErrSnippetIndex is returned when GetSnippet receives an out-of-range
// index. Surfaced when a stale callback_data references a snippet that
// no longer exists (e.g. snippets removed between deploys).
var ErrSnippetIndex = errors.New("quiz: snippet index out of range")

// GetSnippet returns the snippet at idx. Out-of-range returns
// ErrSnippetIndex.
func GetSnippet(idx int) (Snippet, error) {
	if idx < 0 || idx >= len(snippets) {
		return Snippet{}, ErrSnippetIndex
	}
	return snippets[idx], nil
}

// PickRandom returns a random snippet's index. Tests inject a
// deterministic *rand.Rand; in production the callers use rand.New
// seeded from time.Now.
func PickRandom(r *rand.Rand) int {
	if r == nil {
		r = rand.New(rand.NewSource(time.Now().UnixNano()))
	}
	return r.Intn(len(snippets))
}

// BuildOptions returns the four button labels for a snippet: the
// correct language plus three distractors, in randomised order.
// Returns the slice of langs as well as the index (0..3) of the
// correct one in that slice.
func BuildOptions(snippetIdx int, r *rand.Rand) ([]Lang, int, error) {
	s, err := GetSnippet(snippetIdx)
	if err != nil {
		return nil, 0, err
	}
	if r == nil {
		r = rand.New(rand.NewSource(time.Now().UnixNano()))
	}
	var distractors []Lang
	for _, l := range AllLangs {
		if l != s.Answer {
			distractors = append(distractors, l)
		}
	}
	r.Shuffle(len(distractors), func(i, j int) { distractors[i], distractors[j] = distractors[j], distractors[i] })
	picked := distractors[:3]
	options := append([]Lang{s.Answer}, picked...)
	r.Shuffle(len(options), func(i, j int) { options[i], options[j] = options[j], options[i] })
	correctIdx := -1
	for i, l := range options {
		if l == s.Answer {
			correctIdx = i
			break
		}
	}
	return options, correctIdx, nil
}
