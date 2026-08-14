package monthstats

import "regexp"

// The "keyword champion" nomination mirrors the legacy chat-export.org
// "самый курсористый" block: a case-insensitive count of a meme keyword
// per message. The default mirrors the legacy regex exactly
// (?i)курсор|cursor.
const DefaultKeywordPattern = `(?i)курсор|cursor`

var keywordRe = regexp.MustCompile(DefaultKeywordPattern)

// CountKeyword returns the number of non-overlapping keyword matches in
// s. Matching is case-insensitive via the compiled pattern; the legacy
// code lower-cased then matched, which is equivalent.
func CountKeyword(s string) int {
	if s == "" {
		return 0
	}
	return len(keywordRe.FindAllString(s, -1))
}
