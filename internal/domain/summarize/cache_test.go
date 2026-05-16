package summarize

import (
	"testing"
	"time"
)

func TestCache_HitAndMiss(t *testing.T) {
	c := newCache(time.Minute)
	key := cacheKey{chatID: 1, lastMsgID: 42, n: 200, qHash: ""}
	meta := Meta{Included: 10, From: time.Now(), To: time.Now()}

	if _, _, ok := c.get(key); ok {
		t.Fatal("empty cache must miss")
	}

	c.set(key, "summary text", meta)

	body, got, ok := c.get(key)
	if !ok {
		t.Fatal("must hit after set")
	}
	if body != "summary text" {
		t.Fatalf("body = %q, want %q", body, "summary text")
	}
	if got.Included != 10 {
		t.Fatalf("meta.Included = %d, want 10", got.Included)
	}
}

func TestCache_DifferentQuestionsDifferentKeys(t *testing.T) {
	c := newCache(time.Minute)
	base := cacheKey{chatID: 1, lastMsgID: 42, n: 200}
	meta := Meta{Included: 5}

	k1 := base
	k1.qHash = questionsHash("")
	c.set(k1, "base summary", meta)

	k2 := base
	k2.qHash = questionsHash("что решили?")
	if _, _, ok := c.get(k2); ok {
		t.Fatal("different questions must produce cache miss")
	}

	c.set(k2, "answer summary", meta)
	body, _, ok := c.get(k2)
	if !ok || body != "answer summary" {
		t.Fatalf("question-keyed entry must hit: ok=%v body=%q", ok, body)
	}
}

func TestCache_TTLExpiry(t *testing.T) {
	c := newCache(50 * time.Millisecond)
	key := cacheKey{chatID: 1, lastMsgID: 1, n: 100}
	c.set(key, "fresh", Meta{})

	if _, _, ok := c.get(key); !ok {
		t.Fatal("must hit before TTL")
	}
	time.Sleep(60 * time.Millisecond)
	if _, _, ok := c.get(key); ok {
		t.Fatal("must miss after TTL")
	}
}

func TestCache_NewMessageInvalidates(t *testing.T) {
	c := newCache(time.Minute)
	c.set(cacheKey{chatID: 1, lastMsgID: 42, n: 200}, "old summary", Meta{})

	if _, _, ok := c.get(cacheKey{chatID: 1, lastMsgID: 43, n: 200}); ok {
		t.Fatal("different lastMsgID must miss")
	}
}

func TestCache_PrunesExpiredOnSet(t *testing.T) {
	c := newCache(50 * time.Millisecond)
	c.set(cacheKey{chatID: 1, lastMsgID: 1, n: 100}, "a", Meta{})
	c.set(cacheKey{chatID: 2, lastMsgID: 1, n: 100}, "b", Meta{})
	time.Sleep(60 * time.Millisecond)

	c.set(cacheKey{chatID: 3, lastMsgID: 1, n: 100}, "c", Meta{})

	c.mu.Lock()
	n := len(c.entries)
	c.mu.Unlock()
	if n != 1 {
		t.Fatalf("expired entries not pruned: have %d, want 1", n)
	}
}

func TestQuestionsHash_EmptyIsEmpty(t *testing.T) {
	if h := questionsHash(""); h != "" {
		t.Fatalf("empty questions must produce empty hash, got %q", h)
	}
}

func TestQuestionsHash_Deterministic(t *testing.T) {
	h1 := questionsHash("что решили по деплою?")
	h2 := questionsHash("что решили по деплою?")
	if h1 != h2 {
		t.Fatalf("same input must produce same hash: %q vs %q", h1, h2)
	}
	h3 := questionsHash("другой вопрос")
	if h1 == h3 {
		t.Fatal("different inputs must produce different hashes")
	}
}
