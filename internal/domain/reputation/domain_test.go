package reputation

import "testing"

func TestTitleForScoreThresholds(t *testing.T) {
	cases := []struct {
		score int
		want  string
	}{
		{0, "Участник"},
		{11, "Участник"},
		{12, "Дежурный"},
		{35, "Дежурный"},
		{36, "Мастер"},
		{71, "Мастер"},
		{72, "Хранитель прода"},
		{1000, "Хранитель прода"},
		{-5, "Участник"},
	}
	for _, c := range cases {
		if got := TitleForScore(c.score).Name; got != c.want {
			t.Errorf("TitleForScore(%d) = %q, want %q", c.score, got, c.want)
		}
	}
}

func TestNextTitleDistance(t *testing.T) {
	cases := []struct {
		score    int
		wantName string
		wantOk   bool
		wantGap  int
	}{
		{0, "Дежурный", true, 12},
		{11, "Дежурный", true, 1},
		{12, "Мастер", true, 24},
		{36, "Хранитель прода", true, 36},
		{71, "Хранитель прода", true, 1},
		{72, "", false, 0},
	}
	for _, c := range cases {
		next, ok := NextTitle(c.score)
		if ok != c.wantOk {
			t.Errorf("NextTitle(%d) ok = %v, want %v", c.score, ok, c.wantOk)
			continue
		}
		if !ok {
			continue
		}
		if next.Name != c.wantName || next.Threshold-c.score != c.wantGap {
			t.Errorf("NextTitle(%d) = %q in %d, want %q in %d",
				c.score, next.Name, next.Threshold-c.score, c.wantName, c.wantGap)
		}
	}
}

func TestFourPraisesReachFirstThreshold(t *testing.T) {
	// A new player starts at score 0; each valid praise adds three.
	if got := TitleForScore(4 * 3).Name; got != "Дежурный" {
		t.Fatalf("four praises reach %q, want Дежурный", got)
	}
}
