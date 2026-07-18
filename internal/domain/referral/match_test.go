package referral

import "testing"

func TestNormalizeName(t *testing.T) {
	cases := map[string]string{
		"ZAI Coding Plan":  "zaicodingplan",
		"z.ai coding-plan": "zaicodingplan",
		"ZAI codingplan":   "zaicodingplan",
		"  ":               "",
		"":                 "",
		"Claude Code!":     "claudecode",
	}
	for in, want := range cases {
		if got := NormalizeName(in); got != want {
			t.Errorf("NormalizeName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMatchServices(t *testing.T) {
	services := []Service{
		{ID: 1, Name: "ZAI Coding Plan", NameKey: NormalizeName("ZAI Coding Plan")},
		{ID: 2, Name: "Claude Coding Plan", NameKey: NormalizeName("Claude Coding Plan")},
		{ID: 3, Name: "Cursor", NameKey: NormalizeName("Cursor")},
	}

	t.Run("exact variants", func(t *testing.T) {
		for _, q := range []string{"ZAI Coding Plan", "z.ai coding-plan", "ZAI codingplan"} {
			got := MatchServices(q, services)
			if len(got) == 0 || !got[0].Exact {
				t.Fatalf("query %q: expected an exact match first, got %+v", q, got)
			}
			if got[0].Service.ID != 1 {
				t.Errorf("query %q: exact match should be service 1, got %d", q, got[0].Service.ID)
			}
		}
	})

	t.Run("fuzzy typo", func(t *testing.T) {
		got := MatchServices("ZAI Codng Plan", services)
		// Exact must be absent.
		for _, m := range got {
			if m.Exact {
				t.Fatalf("typo query should not be exact, got %+v", m)
			}
		}
		// The closest fuzzy should still be the ZAI service.
		if len(got) == 0 {
			t.Fatal("typo query should yield a fuzzy candidate")
		}
		if got[0].Service.ID != 1 {
			t.Errorf("typo query: top fuzzy should be service 1, got %+v", got[0])
		}
	})

	t.Run("excludes unrelated", func(t *testing.T) {
		got := MatchServices("Claude Coding Plan", services)
		if len(got) == 0 || !got[0].Exact || got[0].Service.ID != 2 {
			t.Fatalf("Claude query: expected exact service 2, got %+v", got)
		}
		// ZAI must not appear in the results for a Claude query.
		for _, m := range got {
			if m.Service.ID == 1 && !m.Exact {
				t.Errorf("Claude query should not fuzzy-match ZAI, got %+v", m)
			}
		}
	})

	t.Run("empty query", func(t *testing.T) {
		if got := MatchServices("", services); len(got) != 0 {
			t.Errorf("empty query should yield no matches, got %+v", got)
		}
	})

	t.Run("exact sorts first", func(t *testing.T) {
		// Build a list where an exact match coexists with fuzzy ones.
		svcs := []Service{
			{ID: 10, Name: "ZAI Coding Plan", NameKey: NormalizeName("ZAI Coding Plan")},
			{ID: 11, Name: "ZAI Coding Planner", NameKey: NormalizeName("ZAI Coding Planner")},
		}
		got := MatchServices("ZAI Coding Plan", svcs)
		if len(got) == 0 || !got[0].Exact {
			t.Fatalf("expected exact match first, got %+v", got)
		}
	})
}
