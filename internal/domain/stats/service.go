package stats

import (
	"context"
	"fmt"
	"log/slog"
	"sort"

	"github.com/veschin/bidlobot/internal/shared"
)

type Service struct {
	buffer *Buffer
	store  Store
	log    *slog.Logger
}

// NewService создаёт сервис статистики с буфером и хранилищем.
func NewService(store Store, buffer *Buffer, log *slog.Logger) *Service {
	return &Service{
		buffer: buffer,
		store:  store,
		log:    log,
	}
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
		"<b>Chat Statistics</b>\n"+
			"Total messages: %s\n"+
			"Total users: %s\n"+
			"Average per user: %s\n",
		shared.FormatNumber(totalMsgs),
		shared.FormatNumber(userCount),
		shared.FormatNumber(avgPerUser),
	)

	if mostActive != nil {
		output += fmt.Sprintf(
			"Most active: User %d (%s messages)\n",
			mostActive.UserID,
			shared.FormatNumber(mostActive.MessageCount),
		)
	}

	if earliest != nil {
		output += fmt.Sprintf(
			"Tracking since: %s",
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
		return "<b>Top Users</b>\nNo data yet.", nil
	}

	sort.Slice(statsList, func(i, j int) bool {
		if statsList[i].MessageCount != statsList[j].MessageCount {
			return statsList[i].MessageCount > statsList[j].MessageCount
		}
		return statsList[i].FirstSeen.Before(statsList[j].FirstSeen)
	})

	output := "<b>Top Users</b>\n"
	limit := 5
	if len(statsList) < limit {
		limit = len(statsList)
	}

	for idx := 0; idx < limit; idx++ {
		s := statsList[idx]
		output += fmt.Sprintf(
			"%d. User %d - %s messages\n",
			idx+1,
			s.UserID,
			shared.FormatNumber(s.MessageCount),
		)
	}

	return output, nil
}

// Today возвращает статистику по сообщениям за текущий день.
func (s *Service) Today(ctx context.Context, absChatID int64) (string, error) {
	totalMsgs, activeUsers := s.buffer.GetTodayByChat(ctx, absChatID)

	output := fmt.Sprintf(
		"<b>Today's Statistics</b>\n"+
			"Messages: %s\n"+
			"Active users: %s",
		shared.FormatNumber(totalMsgs),
		shared.FormatNumber(activeUsers),
	)

	return output, nil
}

// UserStats возвращает персональную статистику пользователя в чате.
// Ранг определяется среди всех пользователей чата по убыванию сообщений.
func (s *Service) UserStats(ctx context.Context, absChatID, userID int64, username string) (string, error) {
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
	for idx, s := range statsList {
		if s.UserID == userID {
			rank = idx + 1
			break
		}
	}

	userDisplay := shared.UserDisplay(username, fmt.Sprintf("%d", userID))

	output := fmt.Sprintf(
		"<b>Stats for %s</b>\n"+
			"Messages: %s\n"+
			"Rank: %d / %d\n"+
			"First seen: %s\n"+
			"Last seen: %s",
		userDisplay,
		shared.FormatNumber(userStats.MessageCount),
		rank,
		len(statsList),
		shared.FormatDate(userStats.FirstSeen),
		shared.FormatDate(userStats.LastSeen),
	)

	return output, nil
}
