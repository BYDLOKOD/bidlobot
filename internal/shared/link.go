package shared

import "strconv"

// supergroupIDFloor is the smallest supergroup id magnitude: every
// supergroup chat id is "-100" followed by the internal id, so
// |chat_id| >= 10^12. Channels share that form, so they are not
// distinguished here; the bot only ever reacts in a supergroup. Legacy
// basic groups (-123456789), private chats and user ids fall below the
// floor and are rejected.
const supergroupIDFloor int64 = 1_000_000_000_000

// MessageLink renders a t.me permalink to one message of a supergroup.
//
// The Bot API has no "message link" method and the bot never resolves a
// chat username (the membership store keeps the title, not a handle), so
// the link uses the private-chat form Telegram clients themselves produce
// for a "Copy Link" in such a chat:
//
//	https://t.me/c/<internal-id>/<message-id>
//
// where the internal id is the supergroup id WITHOUT the "-100" prefix
// (chat -1001234567890 -> 1234567890). The id is chat-internal, so the
// link is only addressable by someone who is in that chat - which is the
// intended scope: the bot posts these links back into the same chat.
//
// threadID is the message thread (forum topic) the message belongs to:
// a topic message is addressed as /c/<id>/<topic-id>/<message-id>. Thread
// 1 is the General topic and 0 means no topic; both use the two-segment
// form. Pass the message's MessageThreadID.
//
// Returns "" for an id below the -100 floor or an unusable message id:
// callers then render the entry without a link rather than a broken one.
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
