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

// captchaCallbackPredicate matches a supergroup callback whose data is in
// the captcha namespace ("cap:"). Registered BEFORE the catch-all "v1:"
// dispatcher so a new member's answer button is never swallowed by the
// "Кнопка устарела" fallback.
func captchaCallbackPredicate() th.Predicate {
	return func(_ context.Context, update telego.Update) bool {
		cb := update.CallbackQuery
		if cb == nil || cb.Message == nil {
			return false
		}
		if cb.Message.GetChat().Type != telego.ChatTypeSupergroup {
			return false
		}
		return strings.HasPrefix(cb.Data, capCBPrefix)
	}
}

// textCommandPredicate matches a leading "/cmd" (optionally "@bot",
// optional args) in Message.Text. telego's CommandEqual compiles to a
// RE2 \w regex which is ASCII-only, so a Cyrillic command like "/итог"
// never matches it; this fills that gap without relying on \w. cmd must
// include the leading slash, e.g. "/итог".
func textCommandPredicate(cmd string) th.Predicate {
	return func(_ context.Context, update telego.Update) bool {
		m := update.Message
		if m == nil || m.Text == "" {
			return false
		}
		f := strings.Fields(m.Text)
		if len(f) == 0 {
			return false
		}
		head := f[0]
		if at := strings.IndexByte(head, '@'); at >= 0 {
			head = head[:at]
		}
		return head == cmd
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
