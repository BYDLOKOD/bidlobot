// Package captcha is the new-member gate: when a user joins a supergroup
// the bot posts a public math puzzle with inline answer buttons. A correct
// answer clears the challenge (and unmutes the newcomer); no answer within
// the timeout gets the user kicked (ban+unban, rejoinable). The feature is
// opt-in via the CAPTCHA_ENABLED env flag.
//
// The package keeps the Store interface and Challenge struct free of telego
// types so the persistence layer is testable with fakes; only the Service
// layer touches telego.
package captcha

import (
	crand "crypto/rand"
	"encoding/hex"
	"fmt"
	"math/rand/v2"
	"time"
)

// Challenge is one open captcha for a (chat, user). Username/FirstName are
// captured at join time so the sweeper can render a readable mention in the
// "kicked" notice without a live API lookup.
type Challenge struct {
	ID            string    `json:"id"`             // 16 hex chars (8 random bytes)
	UserID        int64     `json:"user_id"`        // the newcomer
	Username      string    `json:"username"`       // @handle at join time, may be ""
	FirstName     string    `json:"first_name"`     // display name at join time
	AbsChatID     int64     `json:"abs_chat_id"`    // positive chat id
	Question      string    `json:"question"`       // "3 + 7"
	CorrectAnswer int       `json:"correct_answer"` // 10
	Answers       []int     `json:"answers"`        // shuffled, one == CorrectAnswer
	CreatedAt     time.Time `json:"created_at"`
	ExpiresAt     time.Time `json:"expires_at"` // CreatedAt + timeout
	MessageID     int       `json:"message_id"` // the posted group message (for edit on solve/timeout)
}

// Generate builds a fresh "a + b = ?" challenge. timeout sets ExpiresAt.
// Answers are the correct sum plus three distractors within +/-3 of it,
// clamped at 0 and de-duplicated, then shuffled so the correct value is
// never in a fixed position.
func Generate(userID, absChatID int64, now time.Time, timeout time.Duration) Challenge {
	a := rand.IntN(9) + 1 // 1..9
	b := rand.IntN(9) + 1 // 1..9
	correct := a + b

	answers := make([]int, 0, 4)
	answers = append(answers, correct)
	seen := map[int]bool{correct: true}

	// Up to three distractors in +/-3 of the correct sum. Try a bounded
	// number of draws so generation always terminates even for small
	// sums where the pool is shallow (e.g. sum 2 has only 1,3 nearby).
	for attempts := 0; len(answers) < 4 && attempts < 40; attempts++ {
		delta := rand.IntN(7) - 3 // -3..+3
		if delta == 0 {
			continue
		}
		cand := correct + delta
		if cand < 0 || seen[cand] {
			continue
		}
		seen[cand] = true
		answers = append(answers, cand)
	}

	// Shuffle in place (Fisher-Yates).
	for i := len(answers) - 1; i > 0; i-- {
		j := rand.IntN(i + 1)
		answers[i], answers[j] = answers[j], answers[i]
	}

	return Challenge{
		ID:            newID(),
		UserID:        userID,
		AbsChatID:     absChatID,
		Question:      fmt.Sprintf("%d + %d", a, b),
		CorrectAnswer: correct,
		Answers:       answers,
		CreatedAt:     now.UTC(),
		ExpiresAt:     now.UTC().Add(timeout).UTC(),
	}
}

// newID returns 16 lowercase hex chars from 8 crypto-random bytes. crypto/rand
// is used (not math/rand) so two concurrent joins can never collide on an id
// and a guessable id can't be brute-forced to clear someone else's captcha.
func newID() string {
	var buf [8]byte
	if _, err := crand.Read(buf[:]); err != nil {
		// crypto/rand.Read on Linux only fails on a broken /dev/urandom,
		// which is a fatal system state; treat it as unrecoverable.
		panic("captcha: crypto/rand failed: " + err.Error())
	}
	return hex.EncodeToString(buf[:])
}
