package bot

import (
	"context"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"

	"github.com/veschin/bidlobot/internal/shared"
)

func supergroupPredicate() th.Predicate {
	return func(_ context.Context, update telego.Update) bool {
		if msg := update.Message; msg != nil {
			return msg.Chat.Type == telego.ChatTypeSupergroup
		}
		if cb := update.CallbackQuery; cb != nil {
			if m := cb.Message; m != nil {
				return m.GetChat().Type == telego.ChatTypeSupergroup
			}
		}
		return false
	}
}

func privatePredicate() th.Predicate {
	return func(_ context.Context, update telego.Update) bool {
		if msg := update.Message; msg != nil {
			return msg.Chat.Type == telego.ChatTypePrivate
		}
		if cb := update.CallbackQuery; cb != nil {
			if m := cb.Message; m != nil {
				return m.GetChat().Type == telego.ChatTypePrivate
			}
		}
		return false
	}
}

func notLinkedChannelPredicate() th.Predicate {
	return func(_ context.Context, update telego.Update) bool {
		if msg := update.Message; msg != nil {
			return msg.SenderChat == nil
		}
		return true
	}
}

func notAnonymousAdminPredicate() th.Predicate {
	return func(_ context.Context, update telego.Update) bool {
		if msg := update.Message; msg != nil && msg.From != nil {
			return !shared.IsAnonymousAdmin(msg.From.ID)
		}
		return true
	}
}

func hasFromPredicate() th.Predicate {
	return func(_ context.Context, update telego.Update) bool {
		if msg := update.Message; msg != nil {
			return msg.From != nil && !msg.From.IsBot
		}
		return false
	}
}
