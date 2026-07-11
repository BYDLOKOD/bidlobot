package stats

import (
	"context"
	"fmt"
	"log/slog"
	"sort"

	"github.com/veschin/bidlobot/internal/shared"
)

type Service struct {
	buffer  *Buffer
	store   Store
	display shared.DisplayResolver
	log     *slog.Logger
}

// NewService создаёт сервис статистики с буфером и хранилищем.
// display может быть nil - тогда имена пользователей не подставляются.
func NewService(store Store, buffer *Buffer, display shared.DisplayResolver, log *slog.Logger) *Service {
	return &Service{
		buffer:  buffer,
		store:   store,
		display: display,
		log:     log,
	}
}

func (s *Service) displayFor(ctx context.Context, absChatID, userID int64) string {
	if s.display == nil {
		return fmt.Sprintf("User %d", userID)
	}
	d := s.display.UserDisplay(ctx, absChatID, userID)
	if d == "" {
		return fmt.Sprintf("User %d", userID)
	}
	return d
}

// ChatOverview возвращает HTML-форматированный обзор статистики чата.
func (s *Service) ChatOverview(ctx context.Context, absChatID int64) (string, error) {
	statsList, err := s.buffer.ListMergedByChat(ctx, absChatID)
	if err != nil {
		return "", err
	}

	if len(statsList) == 0 {
		return "<b>Chat Statistics</b>\nNo data yet.", nil
	}

	totalMsgs := int64(0)
	var mostActive *Stats
	var earliest *Stats

	for i := range statsList {
		totalMsgs += statsList[i].MessageCount
		if mostActive == nil || statsList[i].MessageCount > mostActive.MessageCount {
			mostActive = &statsList[i]
		}
		if earliest == nil || statsList[i].FirstSeen.Before(earliest.FirstSeen) {
			earliest = &statsList[i]
		}
	}

	userCount := int64(len(statsList))
	avgPerUser := totalMsgs / userCount

	output := fmt.Sprintf(
		"<b>Статистика чата</b>\n"+
			"Всего сообщений: %s\n"+
			"Всего участников: %s\n"+
			"В среднем на участника: %s\n",
		shared.FormatNumber(totalMsgs),
		shared.FormatNumber(userCount),
		shared.FormatNumber(avgPerUser),
	)

	if mostActive != nil {
		output += fmt.Sprintf(
			"Самый активный: %s (%s сообщений)\n",
			s.displayFor(ctx, absChatID, mostActive.UserID),
			shared.FormatNumber(mostActive.MessageCount),
		)
	}

	if earliest != nil {
		output += fmt.Sprintf(
			"Данные с: %s",
			shared.FormatDate(earliest.FirstSeen),
		)
	}

	return output, nil
}

// Top возвращает топ-5 пользователей по количеству сообщений.
func (s *Service) Top(ctx context.Context, absChatID int64) (string, error) {
	statsList, err := s.buffer.ListMergedByChat(ctx, absChatID)
	if err != nil {
		return "", err
	}

	if len(statsList) == 0 {
		return "<b>Топ участников</b>\nПока нет данных.", nil
	}

	sort.Slice(statsList, func(i, j int) bool {
		if statsList[i].MessageCount != statsList[j].MessageCount {
			return statsList[i].MessageCount > statsList[j].MessageCount
		}
		return statsList[i].FirstSeen.Before(statsList[j].FirstSeen)
	})

	output := "<b>Топ участников</b>\n"
	limit := 5
	if len(statsList) < limit {
		limit = len(statsList)
	}

	for idx := 0; idx < limit; idx++ {
		row := statsList[idx]
		output += fmt.Sprintf(
			"%d. %s - %s сообщений\n",
			idx+1,
			s.displayFor(ctx, absChatID, row.UserID),
			shared.FormatNumber(row.MessageCount),
		)
	}

	return output, nil
}

// Today возвращает статистику по сообщениям за текущий день.
func (s *Service) Today(ctx context.Context, absChatID int64) (string, error) {
	totalMsgs, activeUsers := s.buffer.GetTodayByChat(ctx, absChatID)

	output := fmt.Sprintf(
		"<b>Статистика за сегодня (МСК)</b>\n"+
			"Сообщений: %s\n"+
			"Активных участников: %s",
		shared.FormatNumber(totalMsgs),
		shared.FormatNumber(activeUsers),
	)

	return output, nil
}

// UserStats возвращает персональную статистику пользователя в чате.
// Ранг определяется среди всех пользователей чата по убыванию сообщений.
// Параметр username игнорируется - имя берётся через DisplayResolver.
func (s *Service) UserStats(ctx context.Context, absChatID, userID int64, _ string) (string, error) {
	userStats, err := s.buffer.GetMerged(ctx, userID, absChatID)
	if err == ErrNotFound {
		return "", fmt.Errorf("stat not found: %w", err)
	}
	if err != nil {
		return "", err
	}

	statsList, err := s.buffer.ListMergedByChat(ctx, absChatID)
	if err != nil {
		return "", err
	}

	sort.Slice(statsList, func(i, j int) bool {
		if statsList[i].MessageCount != statsList[j].MessageCount {
			return statsList[i].MessageCount > statsList[j].MessageCount
		}
		return statsList[i].FirstSeen.Before(statsList[j].FirstSeen)
	})

	rank := 0
	for idx, row := range statsList {
		if row.UserID == userID {
			rank = idx + 1
			break
		}
	}

	output := fmt.Sprintf(
		"<b>Статистика: %s</b>\n"+
			"Сообщений: %s\n"+
			"Место: %d из %d\n"+
			"Первое появление: %s\n"+
			"Последняя активность: %s",
		s.displayFor(ctx, absChatID, userID),
		shared.FormatNumber(userStats.MessageCount),
		rank,
		len(statsList),
		shared.FormatDate(userStats.FirstSeen),
		shared.FormatDate(userStats.LastSeen),
	)

	return output, nil
}
