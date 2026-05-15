package monthstats

import (
	"regexp"
	"sync"
)

// The "keyword champion" nomination mirrors the legacy chat-export.org
// "самый курсористый" block: a case-insensitive count of a meme keyword
// per message. The default mirrors the legacy regex exactly
// (?i)курсор|cursor; it is overridable once at startup so the meme of the
// season can change without a code edit, but it is NOT per-request
// (an unbounded user-supplied alternation would be a regex-DoS vector and
// would also make live and import counts diverge).
const DefaultKeywordPattern = `(?i)курсор|cursor`

var (
	keywordMu sync.RWMutex
	keywordRe = regexp.MustCompile(DefaultKeywordPattern)
)

// SetKeywordPattern replaces the compiled keyword regex. It is intended
// to be called at most once, during startup wiring, from config. An
// invalid pattern is rejected and the previous (or default) regex stays
// in force.
func SetKeywordPattern(pattern string) error {
	if pattern == "" {
		pattern = DefaultKeywordPattern
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return err
	}
	keywordMu.Lock()
	keywordRe = re
	keywordMu.Unlock()
	return nil
}

// CountKeyword returns the number of non-overlapping keyword matches in
// s. Matching is case-insensitive via the compiled pattern; the legacy
// code lower-cased then matched, which is equivalent.
func CountKeyword(s string) int {
	if s == "" {
		return 0
	}
	keywordMu.RLock()
	re := keywordRe
	keywordMu.RUnlock()
	return len(re.FindAllString(s, -1))
}
