package referral

import (
	"sort"
	"strings"
	"unicode"
)

// Match is one candidate service for a submitted name. Exact is true
// when the submitted name normalizes identically to the service's
// NameKey. Score is the [0,1] similarity for fuzzy candidates; exact
// matches always carry Score = 1.0.
type Match struct {
	Service Service
	Exact   bool
	Score   float64
}

// NormalizeName returns the lowercased concatenation of every Unicode
// letter and digit in name. Spaces, punctuation, dots, and hyphens are
// discarded, so "ZAI Coding Plan", "z.ai coding-plan", and
// "ZAI codingplan" all reduce to "zaicodingplan". The repository
// recomputes this from the submitted name rather than trusting a
// caller-supplied Service.NameKey.
func NormalizeName(name string) string {
	var b strings.Builder
	b.Grow(len(name))
	for _, r := range name {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(unicode.ToLower(r))
		}
	}
	return b.String()
}

// MatchServices returns exact and fuzzy matches for submitted among
// services. Exact matches lead with Score = 1.0. A fuzzy candidate
// qualifies when any of:
//   - token Jaccard similarity >= 2/3,
//   - rune Levenshtein similarity >= 0.82 with the shorter compact
//     length >= 5, or
//   - compact-prefix ratio >= 0.65 with the shorter compact length
//     >= 4.
//
// Score is the max of those three sub-scores. The slice is sorted by
// Exact, descending Score, then case-insensitive Name. The caller is
// free to truncate (the UI shows at most three).
func MatchServices(submitted string, services []Service) []Match {
	q := NormalizeName(submitted)
	qTokens := tokens(submitted)
	var out []Match
	for _, svc := range services {
		if q != "" && svc.NameKey == q {
			out = append(out, Match{Service: svc, Exact: true, Score: 1.0})
			continue
		}
		score, ok := fuzzyScore(q, qTokens, svc)
		if ok {
			out = append(out, Match{Service: svc, Exact: false, Score: score})
		}
	}
	sortMatches(out)
	return out
}

// fuzzyScore computes the max of three sub-scores between the
// normalized query and one service, and reports whether the candidate
// clears any of the qualifying thresholds.
func fuzzyScore(q string, qTokens map[string]struct{}, svc Service) (float64, bool) {
	s := svc.NameKey
	if q == "" || s == "" {
		return 0, false
	}
	qRunes := []rune(q)
	sRunes := []rune(s)
	maxLen := len(qRunes)
	if len(sRunes) > maxLen {
		maxLen = len(sRunes)
	}
	minLen := len(qRunes)
	if len(sRunes) < minLen {
		minLen = len(sRunes)
	}

	lev := 0.0
	if maxLen > 0 {
		lev = 1 - float64(levenshtein(qRunes, sRunes))/float64(maxLen)
	}
	jac := jaccard(qTokens, tokens(svc.Name))

	cp := 0
	for i := 0; i < len(qRunes) && i < len(sRunes); i++ {
		if qRunes[i] == sRunes[i] {
			cp++
		} else {
			break
		}
	}
	prefix := 0.0
	if minLen > 0 {
		prefix = float64(cp) / float64(minLen)
	}

	score := lev
	if jac > score {
		score = jac
	}
	if prefix > score {
		score = prefix
	}

	switch {
	case jac >= 2.0/3.0:
		return score, true
	case lev >= 0.82 && minLen >= 5:
		return score, true
	case prefix >= 0.65 && minLen >= 4:
		return score, true
	}
	return score, false
}

// tokens splits name into the set of lowercased letter/digit runs.
// "ZAI Coding Plan" -> {"zai","coding","plan"}. Multiple separators
// collapse; an empty/whitespace name yields an empty set.
func tokens(name string) map[string]struct{} {
	out := make(map[string]struct{})
	var b strings.Builder
	for _, r := range name {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(unicode.ToLower(r))
			continue
		}
		if b.Len() > 0 {
			out[b.String()] = struct{}{}
			b.Reset()
		}
	}
	if b.Len() > 0 {
		out[b.String()] = struct{}{}
	}
	return out
}

func jaccard(a, b map[string]struct{}) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	inter := 0
	for t := range a {
		if _, ok := b[t]; ok {
			inter++
		}
	}
	union := len(a) + len(b) - inter
	if union <= 0 {
		return 0
	}
	return float64(inter) / float64(union)
}

// levenshtein is the standard rune-level edit distance.
func levenshtein(a, b []rune) int {
	la, lb := len(a), len(b)
	if la == 0 {
		return lb
	}
	if lb == 0 {
		return la
	}
	prev := make([]int, lb+1)
	curr := make([]int, lb+1)
	for j := 0; j <= lb; j++ {
		prev[j] = j
	}
	for i := 1; i <= la; i++ {
		curr[0] = i
		for j := 1; j <= lb; j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			m := prev[j] + 1
			if v := curr[j-1] + 1; v < m {
				m = v
			}
			if v := prev[j-1] + cost; v < m {
				m = v
			}
			curr[j] = m
		}
		prev, curr = curr, prev
	}
	return prev[lb]
}

func sortMatches(ms []Match) {
	sort.SliceStable(ms, func(i, j int) bool {
		if ms[i].Exact != ms[j].Exact {
			return ms[i].Exact
		}
		if ms[i].Score != ms[j].Score {
			return ms[i].Score > ms[j].Score
		}
		return strings.ToLower(ms[i].Service.Name) < strings.ToLower(ms[j].Service.Name)
	})
}
