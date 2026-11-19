package bot

import (
	"strings"
	"testing"
)

// Guard for the shared corpora used by ReputationHandler and the
// reaction-rep replies: both pools stay large enough for replayability,
// every template carries exactly one %s placeholder, and the quiet
// "unfinished thought" voice (trailing "...") is preserved.
func TestQuipTemplatesCuratedCounts(t *testing.T) {
	if len(roastTemplates) < 35 {
		t.Errorf("roast pool too small for replayability: %d", len(roastTemplates))
	}
	if len(praiseTemplates) < 35 {
		t.Errorf("praise pool too small for replayability: %d", len(praiseTemplates))
	}
	all := append(append([]string{}, roastTemplates...), praiseTemplates...)
	for i, tmpl := range all {
		if strings.Count(tmpl, "%s") != 1 {
			t.Errorf("template %d must contain exactly one %%s placeholder: %q", i, tmpl)
		}
		if !strings.Contains(tmpl, "...") {
			t.Errorf("template %d must contain \"...\" to keep the elliptical voice: %q", i, tmpl)
		}
	}
}
