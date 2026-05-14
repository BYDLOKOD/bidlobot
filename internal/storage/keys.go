package storage

import "fmt"

func StatsKey(userID, absChatID int64) []byte {
	return []byte(fmt.Sprintf("s:%020d:%020d", userID, absChatID))
}

func StatsChatIndex(absChatID, userID int64) []byte {
	return []byte(fmt.Sprintf("sc:%020d:%020d", absChatID, userID))
}

func StatsChatPrefix(absChatID int64) []byte {
	return []byte(fmt.Sprintf("sc:%020d:", absChatID))
}

func WarnKey(uuid string) []byte {
	return []byte(fmt.Sprintf("w:%s", uuid))
}

func WarnTargetIndex(absChatID, targetUserID int64, uuid string) []byte {
	return []byte(fmt.Sprintf("wt:%020d:%020d:%s", absChatID, targetUserID, uuid))
}

func WarnTargetPrefix(absChatID, targetUserID int64) []byte {
	return []byte(fmt.Sprintf("wt:%020d:%020d:", absChatID, targetUserID))
}

func MemberKey(userID, absChatID int64) []byte {
	return []byte(fmt.Sprintf("m:%020d:%020d", userID, absChatID))
}

func MemberChatIndex(absChatID, userID int64) []byte {
	return []byte(fmt.Sprintf("mc:%020d:%020d", absChatID, userID))
}

func MemberChatPrefix(absChatID int64) []byte {
	return []byte(fmt.Sprintf("mc:%020d:", absChatID))
}

func ChatKey(absChatID int64) []byte {
	return []byte(fmt.Sprintf("c:%020d", absChatID))
}

func AbsChatID(chatID int64) int64 {
	if chatID < 0 {
		return -chatID
	}
	return chatID
}

func parseID(b []byte) int64 {
	var n int64
	for _, c := range b {
		if c >= '0' && c <= '9' {
			n = n*10 + int64(c-'0')
		}
	}
	return n
}
