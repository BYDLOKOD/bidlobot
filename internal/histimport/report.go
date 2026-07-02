package histimport

import (
	"html"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/veschin/bidlobot/internal/shared"
)

// FormatCLIReport reproduces the legacy cmd/bidlobot-import stdout report
// verbatim (so the CLI's observable output is unchanged) and appends the
// monthly summary when the import fed monthstats.
func FormatCLIReport(res *Result, threshold time.Duration, dryRun bool) string {
	st := res.Stats
	mode := "WRITE"
	if dryRun {
		mode = "DRY-RUN"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "=== bidlobot-import (%s) ===\n", mode)
	fmt.Fprintf(&b, "Messages parsed : %d\n", st.TotalMessages)
	fmt.Fprintf(&b, "Service events  : %d\n", st.ServiceMsgs)
	fmt.Fprintf(&b, "Skipped         : nil-from=%d non-user-from_id=%d no-timestamp=%d\n",
		st.SkippedNilFrom, st.SkippedNonUser, st.SkippedNoTS)
	fmt.Fprintf(&b, "Unique users    : %d\n", len(st.Users))
	if !st.Earliest.IsZero() {
		fmt.Fprintf(&b, "Date range      : %s .. %s (%s)\n",
			st.Earliest.Format("2006-01-02"), st.Latest.Format("2006-01-02"),
			st.Latest.Sub(st.Earliest).Round(24*time.Hour))
	}

	all := make([]*Aggregate, 0, len(st.Users))
	for _, a := range st.Users {
		all = append(all, a)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Count > all[j].Count })

	b.WriteString("\nTop-10 by message count:\n")
	for i, a := range all {
		if i >= 10 {
			break
		}
		fmt.Fprintf(&b, "  %2d. %-24s %6d  (last %s)\n",
			i+1, truncName(a.FirstName), a.Count, a.MaxTS.Format("2006-01-02"))
	}

	cutoff := st.Latest.Add(-threshold)
	cands := make([]*Aggregate, 0)
	for _, a := range all {
		if a.MaxTS.Before(cutoff) {
			cands = append(cands, a)
		}
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].MaxTS.Before(cands[j].MaxTS) })
	fmt.Fprintf(&b, "\nWould-be cleanup candidates at %s inactivity: %d\n",
		threshold.Round(24*time.Hour), len(cands))
	b.WriteString("(upper bound - export has no reactions; live reaction tracking after import will spare read-only members)\n")
	for i, a := range cands {
		if i >= 20 {
			fmt.Fprintf(&b, "  ... and %d more\n", len(cands)-20)
			break
		}
		fmt.Fprintf(&b, "  %-24s last wrote %s (%d msgs total)\n",
			truncName(a.FirstName), a.MaxTS.Format("2006-01-02"), a.Count)
	}

	if res.MonthlyAccepted > 0 || res.MonthlyDeduped > 0 || res.MonthlySkippedLive > 0 {
		fmt.Fprintf(&b, "\nMonthly stats   : counted=%d deduped(id<=hwm)=%d skipped(live)=%d watermark %d->%d\n",
			res.MonthlyAccepted, res.MonthlyDeduped, res.MonthlySkippedLive,
			res.PriorWatermark, res.NewWatermark)
	}
	if dryRun {
		b.WriteString("\nDRY RUN - nothing written.\n")
	}
	return b.String()
}

// FormatDMReport renders the import outcome for the private console:
// HTML, Russian, neutral register, no decorative emoji - matching the
// dm_text.go copy. It is what an admin sees after a DM upload completes.
func FormatDMReport(res *Result) string {
	st := res.Stats
	var b strings.Builder
	b.WriteString("<b>Импорт истории завершён</b>\n")
	fmt.Fprintf(&b, "Сообщений в экспорте: %s\n", shared.FormatNumber(st.TotalMessages))
	fmt.Fprintf(&b, "Уникальных участников: %d (записано: %d)\n", len(st.Users), res.MembersWritten)
	if !st.Earliest.IsZero() {
		fmt.Fprintf(&b, "Период: %s - %s\n",
			st.Earliest.Format("2006-01-02"), st.Latest.Format("2006-01-02"))
	}

	all := make([]*Aggregate, 0, len(st.Users))
	for _, a := range st.Users {
		all = append(all, a)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Count > all[j].Count })
	b.WriteString("\n<b>Топ-10 по сообщениям</b>\n")
	for i, a := range all {
		if i >= 10 {
			break
		}
		name := a.FirstName
		if name == "" {
			name = fmt.Sprintf("user %d", a.UserID)
		}
		fmt.Fprintf(&b, "%d. %s - %s\n", i+1,
			html.EscapeString(name), shared.FormatNumber(a.Count))
	}

	if res.MonthlyAccepted > 0 || res.MonthlyDeduped > 0 || res.MonthlySkippedLive > 0 {
		fmt.Fprintf(&b, "\n<b>Помесячная статистика</b>\nДобавлено сообщений: %s\n",
			shared.FormatNumber(res.MonthlyAccepted))
		if res.MonthlyDeduped > 0 {
			fmt.Fprintf(&b, "Пропущено (уже загружены ранее): %s\n",
				shared.FormatNumber(res.MonthlyDeduped))
		}
		if res.MonthlySkippedLive > 0 {
			fmt.Fprintf(&b, "Пропущено (уже учтены ботом вживую): %s\n",
				shared.FormatNumber(res.MonthlySkippedLive))
		}
	}
	b.WriteString("\nГотово. Отчёт за месяц: <code>/stats month ГГГГ-ММ</code>")
	return b.String()
}

func truncName(s string) string {
	if s == "" {
		return "(no name)"
	}
	r := []rune(s)
	if len(r) > 24 {
		return string(r[:23]) + "..."
	}
	return s
}
