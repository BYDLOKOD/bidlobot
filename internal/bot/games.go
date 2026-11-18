// Package bot - reputation command wiring.
//
// GamesRegistry is the leftover startup bundle that cmd/bidlobot builds
// and hands to App. The mini-games themselves were removed (TZ CUT-1);
// the only remaining public social commands are the reputation ones
// (/praise, /roast, /rep, /reptop), which still live behind this bundle
// so the cmd wiring and AttachGames signature stay stable.
package bot

import (
	"time"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"
)

// GamesRegistry bundles the public social-command handlers. Any field
// can be nil - registration code checks before wiring.
type GamesRegistry struct {
	// Reputation handles "/praise", "/roast", "/rep" and "/reptop".
	Reputation *ReputationHandler
}

// registerGameRoutes wires the reputation slash handlers into the
// existing supergroup handler tree. Called from registerRoutes so the
// routing table stays in one file.
func registerGameRoutes(sgGroup *th.HandlerGroup, a *App) {
	g := a.games
	if g == nil || g.Reputation == nil {
		return
	}
	sgGroup.HandleMessage(a.gateMsg("roast", 8*time.Second, g.Reputation.HandleRoast), th.CommandEqual("roast"))
	sgGroup.HandleMessage(a.gateMsg("praise", 8*time.Second, g.Reputation.HandlePraise), th.CommandEqual("praise"))
	sgGroup.HandleMessage(a.gateMsg("rep", 5*time.Second, g.Reputation.HandleRep), th.CommandEqual("rep"))
	sgGroup.HandleMessage(a.gateMsg("reptop", 5*time.Second, g.Reputation.HandleRepTop), th.CommandEqual("reptop"))
}

// AdminChecker is the narrow contract handlers need from the admin
// cache - declared here so tests can stub admin status without
// constructing a full AdminCache backed by a Telegram API mock.
type AdminChecker interface {
	IsAdmin(absChatID, userID int64) (bool, error)
}

// emptyKeyboard is an inline markup with no buttons - used to strip a
// keyboard off a message on edit.
func emptyKeyboard() *telego.InlineKeyboardMarkup {
	return &telego.InlineKeyboardMarkup{InlineKeyboard: [][]telego.InlineKeyboardButton{}}
}
