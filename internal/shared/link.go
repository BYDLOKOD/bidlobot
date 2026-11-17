package shared

import "strconv"

// supergroupIDFloor is the smallest supergroup id magnitude: every
// supergroup chat id is "-100" followed by the internal id, so
// |chat_id| >= 10^12. Legacy basic groups (-123456789) and channels
// fall below the floor.
const supergroupIDFloor int64 = 1_000_000_000_000

// MessageLink renders a t.me permalink to one message of a supergroup.
//
// The Bot API has no "message link" method and the bot never resolves a
// chat username (the membership store keeps the title, not a handle), so
// the link uses Telegram's own private-chat form:
//
//	https://t.me/c/<internal-id>/<message-id>
//
// where the internal id is the supergroup id WITHOUT the "-100" prefix
// (chat -1001234567890 -> 1234567890). Telegram clients resolve that
// form for every member of the chat and jump straight to the message;
// a non-member gets "not found", which is the intended behaviour - the
// bot only ever posts these links back into the chat they came from.
//
// threadID is the message thread (forum topic) the message belongs to.
// Telegram addresses a topic message as /c/<id>/<topic-id>/<message-id>;
// topic 1 is the General topic and its links omit the segment. Pass 0
// when the message carries no thread id.
//
// Returns "" when the chat is not a supergroup or the message id is not
// usable: callers then render the entry without a link rather than a
// broken one.
func MessageLink(chatID int64, threadID, messageID int) string {
	if messageID <= 0 || chatID > -supergroupIDFloor {
		return ""
	}
	internal := -chatID - supergroupIDFloor
	if internal <= 0 {
		return ""
	}
	link := "https://t.me/c/" + strconv.FormatInt(internal, 10)
	if threadID > 1 {
		link += "/" + strconv.Itoa(threadID)
	}
	return link + "/" + strconv.Itoa(messageID)
}
