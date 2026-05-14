package profile

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/veschin/bidlobot/internal/shared"
)

func calcTotalExperience(exp []ExpEntry) int {
	if len(exp) == 0 {
		return 0
	}
	earliest := time.Now().Year()
	for _, e := range exp {
		year := extractStartYear(e.Period)
		if year > 0 && year < earliest {
			earliest = year
		}
	}
	total := time.Now().Year() - earliest
	if total < 0 {
		return 0
	}
	return total
}

func extractStartYear(period string) int {
	period = strings.ReplaceAll(period, "~", "")
	period = strings.ReplaceAll(period, " ", "")
	parts := strings.Split(period, "-")
	if len(parts) == 0 {
		parts = strings.Split(period, "-")
	}
	if len(parts) == 0 {
		return 0
	}
	y, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0
	}
	return y
}

type Service struct {
	store Store
	api   shared.TelegramAPI
	fsm   *FSMStore
	log   *slog.Logger
}

func NewService(store Store, api shared.TelegramAPI, log *slog.Logger) *Service {
	return &Service{
		store: store,
		api:   api,
		fsm:   NewFSMStore(),
		log:   log,
	}
}

func (s *Service) FSM() *FSMStore {
	return s.fsm
}

func (s *Service) Create(ctx context.Context, p *Profile) error {
	return s.store.Create(ctx, p)
}

func (s *Service) Get(ctx context.Context, userID, absChatID int64) (*Profile, error) {
	return s.store.Get(ctx, userID, absChatID)
}

func (s *Service) GetByUsername(ctx context.Context, absChatID int64, username string) (*Profile, error) {
	return s.store.GetByUsername(ctx, absChatID, username)
}

func (s *Service) Update(ctx context.Context, p *Profile) error {
	return s.store.Update(ctx, p)
}

func (s *Service) ListByUser(ctx context.Context, userID int64) ([]Profile, error) {
	return s.store.ListByUser(ctx, userID)
}

func (s *Service) FormatProfile(p *Profile) string {
	var b strings.Builder

	b.WriteString(fmt.Sprintf("<b>%s</b>", shared.UserDisplay(p.Username, p.FirstName)))
	if p.Username != "" && p.FirstName != "" {
		b.WriteString(fmt.Sprintf(" (%s)", shared.EscapeHTML(p.FirstName)))
	}
	b.WriteString("\n")

	if len(p.Experience) > 0 {
		totalYears := calcTotalExperience(p.Experience)
		header := "<b>Опыт</b>"
		if totalYears > 0 {
			header += fmt.Sprintf("  <i>~%d лет</i>", totalYears)
		}
		b.WriteString("\n" + header + "\n")
		for _, exp := range p.Experience {
			b.WriteString("  " + shared.EscapeHTML(exp.Title))
			if exp.Period != "" {
				b.WriteString("  <i>" + shared.EscapeHTML(exp.Period) + "</i>")
			}
			b.WriteString("\n")
		}
	}

	if len(p.Stack) > 0 {
		b.WriteString("\n<b>Стек</b>\n")
		b.WriteString("  <code>" + shared.EscapeHTML(strings.Join(p.Stack, ", ")) + "</code>\n")
	}

	if p.Salary != nil {
		b.WriteString("\n<b>ЗП</b>\n")
		b.WriteString("  " + shared.EscapeHTML(p.Salary.Range) + " " + shared.EscapeHTML(p.Salary.Currency))
		if p.Salary.Net {
			b.WriteString(" net")
		}
		b.WriteString("\n")
		if p.Salary.Direction != "" {
			b.WriteString("  " + shared.EscapeHTML(p.Salary.Direction) + "\n")
		}
		if p.Salary.Status != "" {
			b.WriteString("  <i>" + shared.EscapeHTML(p.Salary.Status) + "</i>\n")
		}
	}

	if p.Location != nil {
		b.WriteString("\n<b>Локация</b>\n")
		b.WriteString("  " + shared.EscapeHTML(p.Location.City))
		if p.Location.Timezone != "" {
			b.WriteString(", " + shared.EscapeHTML(p.Location.Timezone))
		}
		b.WriteString("\n")
	}

	if p.Setup != nil {
		b.WriteString("\n<b>Сетап</b>\n")
		if p.Setup.OS != "" {
			b.WriteString("  " + shared.EscapeHTML(p.Setup.OS) + "\n")
		}
		if len(p.Setup.Devices) > 0 {
			b.WriteString("  " + shared.EscapeHTML(strings.Join(p.Setup.Devices, ", ")) + "\n")
		}
		if len(p.Setup.Gaming) > 0 {
			b.WriteString("  " + shared.EscapeHTML(strings.Join(p.Setup.Gaming, ", ")) + "\n")
		}
		if len(p.Setup.AITools) > 0 {
			b.WriteString("  <i>AI:</i> " + shared.EscapeHTML(strings.Join(p.Setup.AITools, ", ")) + "\n")
		}
	}

	if p.Gaming != nil {
		b.WriteString("\n<b>Игры</b>\n")
		if len(p.Gaming.Favorites) > 0 {
			b.WriteString("  " + shared.EscapeHTML(strings.Join(p.Gaming.Favorites, ", ")) + "\n")
		}
		if p.Gaming.Preferences != "" {
			b.WriteString("  <i>" + shared.EscapeHTML(p.Gaming.Preferences) + "</i>\n")
		}
	}

	if len(p.Links) > 0 {
		b.WriteString("\n<b>Ссылки</b>\n")
		for k, v := range p.Links {
			if v != "" && v != "-" {
				b.WriteString("  " + shared.EscapeHTML(k) + ": " + shared.EscapeHTML(v) + "\n")
			}
		}
	}

	if len(p.Socials) > 0 {
		has := false
		for _, v := range p.Socials {
			if v != "" && v != "-" {
				has = true
				break
			}
		}
		if has {
			b.WriteString("\n<b>Соцсети</b>\n")
			for k, v := range p.Socials {
				if v != "" && v != "-" {
					b.WriteString("  " + shared.EscapeHTML(k) + ": " + shared.EscapeHTML(v) + "\n")
				}
			}
		}
	}

	if len(p.Tools) > 0 && (p.Setup == nil || len(p.Setup.AITools) == 0) {
		b.WriteString("\n<b>AI</b>\n")
		b.WriteString("  " + shared.EscapeHTML(strings.Join(p.Tools, ", ")) + "\n")
	}

	if p.Bio != "" {
		b.WriteString("\n<b>О себе</b>\n")
		b.WriteString("  " + shared.EscapeHTML(p.Bio) + "\n")
	}

	if p.Born > 0 {
		age := time.Now().Year() - p.Born
		b.WriteString(fmt.Sprintf("\n<i>%d г.р. (%d лет)</i>", p.Born, age))
	}

	return b.String()
}
