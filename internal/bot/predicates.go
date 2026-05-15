package bot

import (
	"context"
	"strings"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"
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

// dmCallbackPredicate matches callback queries from a private chat
// whose data is in the DM-console namespace. Keeps DM callbacks off the
// public dispatcher and vice versa.
func dmCallbackPredicate() th.Predicate {
	return func(_ context.Context, update telego.Update) bool {
		cb := update.CallbackQuery
		if cb == nil || cb.Message == nil {
			return false
		}
		if cb.Message.GetChat().Type != telego.ChatTypePrivate {
			return false
		}
		return strings.HasPrefix(cb.Data, dmCBNamespace)
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
