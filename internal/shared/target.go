package shared

import (
	"errors"
	"strconv"
	"strings"

	"github.com/mymmrac/telego"
)

var errNoTarget = errors.New("no target specified")

type Target struct {
	UserID      int64
	Username    string
	DisplayName string
}

func ResolveTarget(msg *telego.Message) (target Target, reason string, err error) {
	if msg.ReplyToMessage != nil && msg.ReplyToMessage.From != nil {
		from := msg.ReplyToMessage.From
		target = Target{
			UserID:      from.ID,
			Username:    from.Username,
			DisplayName: displayName(from),
		}
		reason = strings.Join(commandArgs(msg.Text), " ")
		return
	}

	args := commandArgs(msg.Text)
	if len(args) == 0 {
		return target, "", errNoTarget
	}

	first := args[0]
	if strings.HasPrefix(first, "@") {
		target.Username = strings.TrimPrefix(first, "@")
		target.DisplayName = first
	} else if uid, parseErr := strconv.ParseInt(first, 10, 64); parseErr == nil {
		target.UserID = uid
		target.DisplayName = first
	} else {
		return target, "", errNoTarget
	}

	if len(args) > 1 {
		reason = strings.Join(args[1:], " ")
	}
	return
}

func commandArgs(text string) []string {
	parts := strings.Fields(text)
	if len(parts) <= 1 {
		return nil
	}
	return parts[1:]
}


func displayName(u *telego.User) string {
	if u.Username != "" {
		return "@" + u.Username
	}
	return u.FirstName
}

