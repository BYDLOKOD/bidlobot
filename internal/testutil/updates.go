package testutil

import "github.com/mymmrac/telego"

var msgCounter int

func nextMsgID() int {
	msgCounter++
	return msgCounter
}

func SupergroupMessage(chatID int64, from *telego.User, text string) telego.Message {
	return telego.Message{
		MessageID: nextMsgID(),
		From:      from,
		Chat:      telego.Chat{ID: chatID, Type: telego.ChatTypeSupergroup},
		Text:      text,
		Date:      1700000000,
	}
}

func PrivateMessage(from *telego.User, text string) telego.Message {
	return telego.Message{
		MessageID: nextMsgID(),
		From:      from,
		Chat:      telego.Chat{ID: from.ID, Type: telego.ChatTypePrivate},
		Text:      text,
		Date:      1700000000,
	}
}

func ReplyMessage(chatID int64, from *telego.User, text string, replyTo *telego.Message) telego.Message {
	msg := SupergroupMessage(chatID, from, text)
	msg.ReplyToMessage = replyTo
	return msg
}

func CallbackQuery(from *telego.User, chatID int64, msgID int, data string) telego.CallbackQuery {
	return telego.CallbackQuery{
		ID:   "cb_test",
		From: *from,
		Message: &telego.InaccessibleMessage{
			MessageID: msgID,
			Chat:      telego.Chat{ID: chatID, Type: telego.ChatTypePrivate},
			Date:      0,
		},
		Data: data,
	}
}

func User(id int64, username, firstName string) *telego.User {
	return &telego.User{
		ID:        id,
		Username:  username,
		FirstName: firstName,
	}
}

func Bot(id int64, username string) *telego.User {
	return &telego.User{
		ID:        id,
		Username:  username,
		FirstName: username,
		IsBot:     true,
	}
}
