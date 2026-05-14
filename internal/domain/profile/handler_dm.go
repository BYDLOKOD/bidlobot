package profile

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"
	tu "github.com/mymmrac/telego/telegoutil"

	"github.com/veschin/bidlobot/internal/text"
)

func (h *Handler) HandleStartDM(ctx *th.Context, msg telego.Message) error {
	userID := msg.From.ID
	chatID := msg.Chat.ID

	payload := ""
	if strings.HasPrefix(msg.Text, "/start ") {
		payload = strings.TrimSpace(msg.Text[7:])
	}

	if payload == "" {
		_, err := ctx.Bot().SendMessage(ctx.Context(), &telego.SendMessageParams{
			ChatID:    tu.ID(chatID),
			Text:      text.MsgWelcomeDM,
			ParseMode: "HTML",
		})
		return err
	}

	parts := strings.SplitN(payload, "_", 2)
	if len(parts) != 2 {
		_, err := ctx.Bot().SendMessage(ctx.Context(), &telego.SendMessageParams{
			ChatID:    tu.ID(chatID),
			Text:      text.MsgWelcomeDM,
			ParseMode: "HTML",
		})
		return err
	}

	action := parts[0]
	chatIDStr := parts[1]

	targetChatID, err := strconv.ParseInt(chatIDStr, 10, 64)
	if err != nil {
		_, sendErr := ctx.Bot().SendMessage(ctx.Context(), &telego.SendMessageParams{
			ChatID:    tu.ID(chatID),
			Text:      text.MsgWelcomeDM,
			ParseMode: "HTML",
		})
		return sendErr
	}

	if action == "reg" {
		if h.svc.fsm.Has(userID) {
			_, err := ctx.Bot().SendMessage(ctx.Context(), &telego.SendMessageParams{
				ChatID:    tu.ID(chatID),
				Text:      text.ErrActiveSession,
				ParseMode: "HTML",
			})
			return err
		}

		realChatID := -targetChatID
		isMember, err := h.isMember(ctx.Context(), userID, realChatID)
		if err != nil {
			_, sendErr := ctx.Bot().SendMessage(ctx.Context(), &telego.SendMessageParams{
				ChatID: tu.ID(chatID),
				Text:   text.ErrBotNotMember,
			})
			return sendErr
		}

		if !isMember {
			_, err := ctx.Bot().SendMessage(ctx.Context(), &telego.SendMessageParams{
				ChatID:    tu.ID(chatID),
				Text:      text.ErrNotMember,
				ParseMode: "HTML",
			})
			return err
		}

		profiles, err := h.svc.ListByUser(ctx.Context(), userID)
		if err != nil {
			return err
		}

		if len(profiles) > 0 {
			var rows [][]telego.InlineKeyboardButton
			for _, p := range profiles {
				btnText := fmt.Sprintf(text.BtnCopy, p.FirstName)
				rows = append(rows, tu.InlineKeyboardRow(
					telego.InlineKeyboardButton{
						Text:         btnText,
						CallbackData: fmt.Sprintf("copy:%d", p.ChatID),
					},
				))
			}
			rows = append(rows, tu.InlineKeyboardRow(
				telego.InlineKeyboardButton{
					Text:         text.BtnFill,
					CallbackData: "fill",
				},
			))
			rows = append(rows, tu.InlineKeyboardRow(
				telego.InlineKeyboardButton{
					Text:         text.BtnCancel,
					CallbackData: "cancel",
				},
			))

			_, err := ctx.Bot().SendMessage(ctx.Context(), &telego.SendMessageParams{
				ChatID:      tu.ID(chatID),
				Text:        "Choose a profile to copy from or fill manually:",
				ReplyMarkup: tu.InlineKeyboard(rows...),
			})
			return err
		}

		session := &Session{
			ChatID: targetChatID,
			Step:   StepStack,
			Mode:   ModeRegister,
		}
		h.svc.fsm.Set(userID, session)

		_, err = ctx.Bot().SendMessage(ctx.Context(), &telego.SendMessageParams{
			ChatID:      tu.ID(chatID),
			Text:        text.PromptStack,
			ReplyMarkup: tu.InlineKeyboard(tu.InlineKeyboardRow(telego.InlineKeyboardButton{Text: text.BtnCancel, CallbackData: "cancel"})),
		})
		return err
	}

	if action == "upd" {
		profile, err := h.svc.Get(ctx.Context(), userID, targetChatID)
		if err != nil {
			_, sendErr := ctx.Bot().SendMessage(ctx.Context(), &telego.SendMessageParams{
				ChatID:    tu.ID(chatID),
				Text:      text.ErrNoOwnProfile,
				ParseMode: "HTML",
			})
			return sendErr
		}

		expStr := ""
		if len(profile.Experience) > 0 {
			expStr = profile.Experience[0].Title
		}
		session := &Session{
			ChatID:     targetChatID,
			Step:       StepStack,
			Mode:       ModeUpdate,
			Stack:      strings.Join(profile.Stack, ", "),
			Experience: expStr,
			Bio:        profile.Bio,
		}
		h.svc.fsm.Set(userID, session)

		_, err = ctx.Bot().SendMessage(ctx.Context(), &telego.SendMessageParams{
			ChatID:      tu.ID(chatID),
			Text:        fmt.Sprintf("%s\nCurrent: %s", text.PromptStack, strings.Join(profile.Stack, ", ")),
			ReplyMarkup: tu.InlineKeyboard(tu.InlineKeyboardRow(telego.InlineKeyboardButton{Text: text.BtnCancel, CallbackData: "cancel"})),
		})
		return err
	}

	_, err = ctx.Bot().SendMessage(ctx.Context(), &telego.SendMessageParams{
		ChatID:    tu.ID(chatID),
		Text:      text.MsgWelcomeDM,
		ParseMode: "HTML",
	})
	return err
}

func (h *Handler) HandleCancelDM(ctx *th.Context, msg telego.Message) error {
	userID := msg.From.ID
	chatID := msg.Chat.ID

	if h.svc.fsm.Has(userID) {
		h.svc.fsm.Delete(userID)
		_, err := ctx.Bot().SendMessage(ctx.Context(), &telego.SendMessageParams{
			ChatID:    tu.ID(chatID),
			Text:      text.MsgRegCancelled,
			ParseMode: "HTML",
		})
		return err
	}

	_, err := ctx.Bot().SendMessage(ctx.Context(), &telego.SendMessageParams{
		ChatID:    tu.ID(chatID),
		Text:      text.MsgNothingToCancel,
		ParseMode: "HTML",
	})
	return err
}

func (h *Handler) HandleFSMInput(ctx *th.Context, msg telego.Message) error {
	userID := msg.From.ID
	chatID := msg.Chat.ID

	session, ok := h.svc.fsm.Get(userID)
	if !ok {
		_, err := ctx.Bot().SendMessage(ctx.Context(), &telego.SendMessageParams{
			ChatID: tu.ID(chatID),
			Text:   text.MsgWelcomeDM,
		})
		return err
	}

	input := msg.Text

	switch session.Step {
	case StepStack:
		session.Stack = input
		session.Step = StepExperience
		h.svc.fsm.Set(userID, session)

		_, err := ctx.Bot().SendMessage(ctx.Context(), &telego.SendMessageParams{
			ChatID: tu.ID(chatID),
			Text:   text.PromptExperience,
			ReplyMarkup: tu.InlineKeyboard(tu.InlineKeyboardRow(
				telego.InlineKeyboardButton{Text: text.BtnBack, CallbackData: "back"},
				telego.InlineKeyboardButton{Text: text.BtnCancel, CallbackData: "cancel"},
			)),
		})
		return err

	case StepExperience:
		session.Experience = input
		session.Step = StepBio
		h.svc.fsm.Set(userID, session)

		_, err := ctx.Bot().SendMessage(ctx.Context(), &telego.SendMessageParams{
			ChatID: tu.ID(chatID),
			Text:  text.PromptBio,
			ReplyMarkup: tu.InlineKeyboard(
				tu.InlineKeyboardRow(telego.InlineKeyboardButton{Text: text.BtnSkip, CallbackData: "skip"}),
				tu.InlineKeyboardRow(telego.InlineKeyboardButton{Text: text.BtnCancel, CallbackData: "cancel"}),
			),
		})
		return err

	case StepBio:
		session.Bio = input
		session.Step = StepConfirm
		h.svc.fsm.Set(userID, session)
		return h.showConfirm(ctx.Context(), ctx.Bot(), chatID, userID, session)

	default:
		_, err := ctx.Bot().SendMessage(ctx.Context(), &telego.SendMessageParams{
			ChatID:    tu.ID(chatID),
			Text:      text.MsgSendText,
			ParseMode: "HTML",
		})
		return err
	}
}

func (h *Handler) HandleFSMNonText(ctx *th.Context, msg telego.Message) error {
	userID := msg.From.ID
	chatID := msg.Chat.ID

	if !h.svc.fsm.Has(userID) {
		return nil
	}

	_, err := ctx.Bot().SendMessage(ctx.Context(), &telego.SendMessageParams{
		ChatID:    tu.ID(chatID),
		Text:      text.MsgSendText,
		ParseMode: "HTML",
	})
	return err
}

func (h *Handler) HandleFSMCallback(ctx *th.Context, query telego.CallbackQuery) error {
	userID := query.From.ID
	chatID := query.Message.GetChat().ID
	data := query.Data

	_ = ctx.Bot().AnswerCallbackQuery(ctx.Context(), &telego.AnswerCallbackQueryParams{
		CallbackQueryID: query.ID,
	})

	session, ok := h.svc.fsm.Get(userID)
	if !ok {
		_, err := ctx.Bot().SendMessage(ctx.Context(), &telego.SendMessageParams{
			ChatID:    tu.ID(chatID),
			Text:      text.ErrSessionExpired,
			ParseMode: "HTML",
		})
		return err
	}

	if data == "cancel" {
		h.svc.fsm.Delete(userID)
		_, err := ctx.Bot().SendMessage(ctx.Context(), &telego.SendMessageParams{
			ChatID:    tu.ID(chatID),
			Text:      text.MsgRegCancelled,
			ParseMode: "HTML",
		})
		return err
	}

	if data == "back" {
		if session.Step > StepStack {
			session.Step--
			h.svc.fsm.Set(userID, session)
		}
		return h.showPrompt(ctx.Context(), ctx.Bot(), chatID, userID, session)
	}

	if data == "skip" && session.Step == StepBio {
		session.Bio = ""
		session.Step = StepConfirm
		h.svc.fsm.Set(userID, session)
		return h.showConfirm(ctx.Context(), ctx.Bot(), chatID, userID, session)
	}

	if data == "confirm" {
		stackSlice := []string{}
		if session.Stack != "" {
			stackSlice = strings.Split(session.Stack, ", ")
		}
		experience := []ExpEntry{}
		if session.Experience != "" {
			experience = append(experience, ExpEntry{Title: session.Experience, Period: "present"})
		}
		profile := &Profile{
			UserID:     userID,
			ChatID:     session.ChatID,
			Stack:      stackSlice,
			Experience: experience,
			Bio:        session.Bio,
			Username:   query.From.Username,
			FirstName:  query.From.FirstName,
			CreatedAt:  time.Now(),
			UpdatedAt:  time.Now(),
		}

		if session.Mode == ModeUpdate {
			existing, err := h.svc.Get(ctx.Context(), userID, session.ChatID)
			if err == nil {
				profile.Username = existing.Username
				profile.FirstName = existing.FirstName
				profile.CreatedAt = existing.CreatedAt
			}
			if err := h.svc.Update(ctx.Context(), profile); err != nil {
				return err
			}
		} else {
			if err := h.svc.Create(ctx.Context(), profile); err != nil {
				return err
			}
		}

		h.svc.fsm.Delete(userID)

		msg := text.MsgProfileCreated
		if session.Mode == ModeUpdate {
			msg = text.MsgProfileUpdated
		}

		_, err := ctx.Bot().SendMessage(ctx.Context(), &telego.SendMessageParams{
			ChatID:    tu.ID(chatID),
			Text:      msg,
			ParseMode: "HTML",
		})
		return err
	}

	if strings.HasPrefix(data, "copy:") {
		copyFromChatID, err := strconv.ParseInt(strings.TrimPrefix(data, "copy:"), 10, 64)
		if err != nil {
			return err
		}

		copyFrom, err := h.svc.Get(ctx.Context(), userID, copyFromChatID)
		if err != nil {
			_, sendErr := ctx.Bot().SendMessage(ctx.Context(), &telego.SendMessageParams{
				ChatID:    tu.ID(chatID),
				Text:      text.ErrProfileNotFound,
				ParseMode: "HTML",
			})
			return sendErr
		}

		session.Stack = strings.Join(copyFrom.Stack, ", ")
		if len(copyFrom.Experience) > 0 {
			session.Experience = copyFrom.Experience[0].Title
		}
		session.Bio = copyFrom.Bio
		session.Step = StepConfirm
		h.svc.fsm.Set(userID, session)

		return h.showConfirm(ctx.Context(), ctx.Bot(), chatID, userID, session)
	}

	if data == "fill" {
		session.Step = StepStack
		h.svc.fsm.Set(userID, session)
		_, err := ctx.Bot().SendMessage(ctx.Context(), &telego.SendMessageParams{
			ChatID:      tu.ID(chatID),
			Text:        text.PromptStack,
			ReplyMarkup: tu.InlineKeyboard(tu.InlineKeyboardRow(telego.InlineKeyboardButton{Text: text.BtnCancel, CallbackData: "cancel"})),
		})
		return err
	}

	return nil
}

func (h *Handler) showPrompt(ctx context.Context, bot *telego.Bot, chatID int64, userID int64, session *Session) error {
	var msg string
	var keyboard *telego.InlineKeyboardMarkup

	switch session.Step {
	case StepStack:
		msg = text.PromptStack
		if session.Stack != "" {
			msg += "\nCurrent: " + session.Stack
		}
		keyboard = tu.InlineKeyboard(tu.InlineKeyboardRow(telego.InlineKeyboardButton{Text: text.BtnCancel, CallbackData: "cancel"}))
	case StepExperience:
		msg = text.PromptExperience
		if session.Experience != "" {
			msg += "\nCurrent: " + session.Experience
		}
		keyboard = tu.InlineKeyboard(
			tu.InlineKeyboardRow(
				telego.InlineKeyboardButton{Text: text.BtnBack, CallbackData: "back"},
				telego.InlineKeyboardButton{Text: text.BtnCancel, CallbackData: "cancel"},
			),
		)
	case StepBio:
		msg = text.PromptBio
		if session.Bio != "" {
			msg += "\nCurrent: " + session.Bio
		}
		keyboard = tu.InlineKeyboard(
			tu.InlineKeyboardRow(telego.InlineKeyboardButton{Text: text.BtnSkip, CallbackData: "skip"}),
			tu.InlineKeyboardRow(telego.InlineKeyboardButton{Text: text.BtnCancel, CallbackData: "cancel"}),
		)
	default:
		return nil
	}

	_, err := bot.SendMessage(ctx, &telego.SendMessageParams{
		ChatID:      tu.ID(chatID),
		Text:        msg,
		ReplyMarkup: keyboard,
	})
	return err
}

func (h *Handler) showConfirm(ctx context.Context, bot *telego.Bot, chatID int64, userID int64, session *Session) error {
	confirm := fmt.Sprintf(
		"<b>%s</b>\n<b>Stack:</b> %s\n<b>Experience:</b> %s",
		text.PromptConfirm,
		session.Stack,
		session.Experience,
	)
	if session.Bio != "" {
		confirm += fmt.Sprintf("\n<b>Bio:</b> %s", session.Bio)
	}

	keyboard := tu.InlineKeyboard(
		tu.InlineKeyboardRow(
			telego.InlineKeyboardButton{Text: text.BtnBack, CallbackData: "back"},
			telego.InlineKeyboardButton{Text: text.BtnConfirm, CallbackData: "confirm"},
		),
	)

	_, err := bot.SendMessage(ctx, &telego.SendMessageParams{
		ChatID:      tu.ID(chatID),
		Text:        confirm,
		ParseMode:   "HTML",
		ReplyMarkup: keyboard,
	})
	return err
}

func (h *Handler) isMember(ctx context.Context, userID int64, chatID int64) (bool, error) {
	member, err := h.bot.GetChatMember(ctx, &telego.GetChatMemberParams{
		ChatID: tu.ID(chatID),
		UserID: userID,
	})
	if err != nil {
		return false, err
	}

	status := member.MemberStatus()
	return status == "member" || status == "administrator" || status == "creator", nil
}
