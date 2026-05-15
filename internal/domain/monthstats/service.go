package monthstats

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/veschin/bidlobot/internal/shared"
)

// DisplayResolver returns a chat-local display name (e.g. "@alice" or
// "Alice") for a user. A nil resolver falls back to "User <id>". Same
// shape as stats.DisplayResolver; an adapter is wired in cmd/bidlobot.
type DisplayResolver interface {
	UserDisplay(ctx context.Context, absChatID, userID int64) string
}

// Service renders the legacy chat-export.org monthly nominations and owns
// the seal/memoization lifecycle: a past (immutable) month is rendered
// once and cached; the in-progress month is rendered fresh from the
// DB+buffer merge every call. A cached summary is auto-invalidated when a
// later import advances MonthState.UpdatedAt, so no explicit cache-bust
// is needed on re-import.
type Service struct {
	store   Store
	buffer  *Buffer
	display DisplayResolver
	log     *slog.Logger
	now     func() time.Time // injectable clock for deterministic seal tests
}

func NewService(store Store, buffer *Buffer, display DisplayResolver, log *slog.Logger) *Service {
	return &Service{
		store:   store,
		buffer:  buffer,
		display: display,
		log:     log,
		now:     func() time.Time { return time.Now().UTC() },
	}
}

func (s *Service) currentMonth() string { return s.now().UTC().Format("2006-01") }

func (s *Service) displayFor(ctx context.Context, abs, uid int64) string {
	if s.display == nil {
		return fmt.Sprintf("User %d", uid)
	}
	if d := s.display.UserDisplay(ctx, abs, uid); d != "" {
		return d
	}
	return fmt.Sprintf("User %d", uid)
}

// Months lists every month the chat has data for, newest first.
func (s *Service) Months(ctx context.Context, absChatID int64) (string, error) {
	months, err := s.buffer.ListMergedMonths(ctx, absChatID)
	if err != nil {
		return "", err
	}
	if len(months) == 0 {
		return "<b>Помесячная статистика</b>\nПока нет данных. История чата не загружена, а живой учёт ещё не накопил ни одного полного месяца.", nil
	}
	cur := s.currentMonth()
	var b strings.Builder
	b.WriteString("<b>Помесячная статистика</b>\nДоступные месяцы (свежие сверху):\n")
	for i := len(months) - 1; i >= 0; i-- {
		m := months[i]
		tag := ""
		if m >= cur {
			tag = " - идёт сейчас"
		}
		fmt.Fprintf(&b, "- <code>%s</code>%s\n", m, tag)
	}
	b.WriteString("\nОтчёт за месяц: <code>/stats month ГГГГ-ММ</code>")
	return b.String(), nil
}

// MonthReport returns the rendered nominations board for one month.
// month "" defaults to the last complete month (or the current one if no
// prior month exists).
func (s *Service) MonthReport(ctx context.Context, absChatID int64, month string) (string, error) {
	if month == "" {
		var err error
		month, err = s.defaultMonth(ctx, absChatID)
		if err != nil {
			return "", err
		}
	}
	cur := s.currentMonth()

	// In-progress month: always fresh, never memoized.
	if month >= cur {
		meta, users, err := s.buffer.GetMergedMonth(ctx, absChatID, month)
		if err != nil {
			return "", err
		}
		return s.render(ctx, absChatID, month, meta, users, true), nil
	}

	// Immutable (past) month: serve the memoized summary unless a later
	// import invalidated it (BuiltAt before the chat's import UpdatedAt)
	// or the render schema changed.
	var stUpdated time.Time
	if st, err := s.store.GetState(ctx, absChatID); err == nil && st != nil {
		stUpdated = st.UpdatedAt
	}
	if sum, err := s.store.GetSummary(ctx, absChatID, month); err == nil && sum != nil &&
		sum.SchemaVer == SummarySchemaVer &&
		(stUpdated.IsZero() || !sum.BuiltAt.Before(stUpdated)) {
		return sum.HTML, nil
	}

	meta, users, err := s.buffer.GetMergedMonth(ctx, absChatID, month)
	if err != nil {
		return "", err
	}
	html := s.render(ctx, absChatID, month, meta, users, false)
	if perr := s.store.PutSummary(ctx, &MonthSummary{
		AbsChatID: absChatID, Month: month, HTML: html,
		BuiltAt: s.now().UTC(), SchemaVer: SummarySchemaVer,
	}); perr != nil {
		s.log.Warn("monthstats summary memoize failed", "error", perr, "chat", absChatID, "month", month)
	}
	return html, nil
}

func (s *Service) defaultMonth(ctx context.Context, absChatID int64) (string, error) {
	months, err := s.buffer.ListMergedMonths(ctx, absChatID)
	if err != nil {
		return "", err
	}
	if len(months) == 0 {
		return s.currentMonth(), nil
	}
	cur := s.currentMonth()
	// Prefer the newest month strictly before the current one.
	for i := len(months) - 1; i >= 0; i-- {
		if months[i] < cur {
			return months[i], nil
		}
	}
	return months[len(months)-1], nil
}

// sortRanked sorts a copy of users by metric desc, tie-broken by earlier
// FirstSeen (mirrors stats.Service deterministic ordering), and returns
// the top n. zerosDrop removes entries whose metric is 0 (legacy
// `remove zero?`, applied only to entity/keyword nominations).
func sortRanked(users []MonthUserStat, metric func(MonthUserStat) int64, n int, zerosDrop bool) []MonthUserStat {
	cp := make([]MonthUserStat, 0, len(users))
	for _, u := range users {
		if zerosDrop && metric(u) == 0 {
			continue
		}
		cp = append(cp, u)
	}
	sort.SliceStable(cp, func(i, j int) bool {
		mi, mj := metric(cp[i]), metric(cp[j])
		if mi != mj {
			return mi > mj
		}
		return cp[i].FirstSeen.Before(cp[j].FirstSeen)
	})
	if len(cp) > n {
		cp = cp[:n]
	}
	return cp
}

func pct(part, total int64) int64 {
	if total <= 0 {
		return 0
	}
	return part * 100 / total // integer truncation, matches legacy (int (* 100 (/ ..)))
}

func (s *Service) section(ctx context.Context, b *strings.Builder, abs int64, title string, users []MonthUserStat, metric func(MonthUserStat) int64, total int64, withPct, zerosDrop bool) {
	top := sortRanked(users, metric, 10, zerosDrop)
	if len(top) == 0 {
		return
	}
	fmt.Fprintf(b, "\n<b>%s</b>\n", title)
	for i, u := range top {
		name := shared.EscapeHTML(s.displayFor(ctx, abs, u.UserID))
		if withPct {
			fmt.Fprintf(b, "%d. %s - %s (%d%%)\n", i+1, name,
				shared.FormatNumber(metric(u)), pct(metric(u), total))
		} else {
			fmt.Fprintf(b, "%d. %s - %s\n", i+1, name, shared.FormatNumber(metric(u)))
		}
	}
}

func (s *Service) render(ctx context.Context, abs int64, month string, meta *MonthMeta, users []MonthUserStat, inProgress bool) string {
	if meta == nil {
		meta = &MonthMeta{AbsChatID: abs, Month: month}
	}
	var b strings.Builder
	tag := ""
	if inProgress {
		tag = " (идёт сейчас)"
	}
	fmt.Fprintf(&b, "<b>📊 Итоги месяца %s%s</b>\n", month, tag)

	if len(users) == 0 || meta.TotalMsgs == 0 {
		b.WriteString("\nЗа этот месяц нет данных.")
		return b.String()
	}

	active := len(users)
	twentyPlus := 0
	for _, u := range users {
		if u.MsgCount > 20 { // legacy code is strictly >20 (header says "20+")
			twentyPlus++
		}
	}
	// Section titles are the user's own coined nominations from
	// chat-export.org, kept verbatim - the crude register is the
	// "БЫДЛОКОД" chat culture and is deliberate, not to be sanitized.
	fmt.Fprintf(&b, "\nВсего сообщений: <b>%s</b>\nУникальных за период: <b>%d</b> (из них 20+ сообщений: <b>%d</b>)\n",
		shared.FormatNumber(meta.TotalMsgs), active, twentyPlus)

	s.section(ctx, &b, abs, "Самый срущий автор", users,
		func(u MonthUserStat) int64 { return u.MsgCount }, meta.TotalMsgs, true, false)
	s.section(ctx, &b, abs, "Самый срущий автор по длине сообщения", users,
		func(u MonthUserStat) int64 { return u.RuneCount }, meta.TotalRunes, true, false)

	if meta.LongestRunes > 0 {
		name := shared.EscapeHTML(s.displayFor(ctx, abs, meta.LongestUserID))
		ex := shared.EscapeHTML(meta.LongestExcerpt)
		cut := ""
		if !meta.LongestFull {
			cut = " <i>(обрезано)</i>"
		}
		fmt.Fprintf(&b, "\n<b>Самое длинное сообщение</b>\n%s - %s символов%s\n<blockquote>%s</blockquote>\n",
			name, shared.FormatNumber(meta.LongestRunes), cut, ex)
	}

	s.section(ctx, &b, abs, "Самый кодирующий автор", users,
		func(u MonthUserStat) int64 { return u.Code }, 0, false, true)
	s.section(ctx, &b, abs, "Самый емоджинутый автор", users,
		func(u MonthUserStat) int64 { return u.CustomEmoji }, 0, false, true)
	s.section(ctx, &b, abs, "Самый тегающий автор", users,
		func(u MonthUserStat) int64 { return u.Mention }, 0, false, true)
	s.section(ctx, &b, abs, "Говорящие с ботами", users,
		func(u MonthUserStat) int64 { return u.BotCommand }, 0, false, true)
	s.section(ctx, &b, abs, "Самый курсористый тип", users,
		func(u MonthUserStat) int64 { return u.KeywordCount }, 0, false, true)

	return b.String()
}
