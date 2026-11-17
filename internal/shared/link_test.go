package shared

import "testing"

func TestMessageLink(t *testing.T) {
	tests := []struct {
		name      string
		chatID    int64
		threadID  int
		messageID int
		want      string
	}{
		{
			name:      "supergroup strips the -100 prefix",
			chatID:    -1001234567890,
			messageID: 42,
			want:      "https://t.me/c/1234567890/42",
		},
		{
			name:      "topic message carries the topic id",
			chatID:    -1001234567890,
			threadID:  17,
			messageID: 42,
			want:      "https://t.me/c/1234567890/17/42",
		},
		{
			name:      "general topic (id 1) omits the segment",
			chatID:    -1001234567890,
			threadID:  1,
			messageID: 42,
			want:      "https://t.me/c/1234567890/42",
		},
		{
			name:      "shortest possible supergroup id",
			chatID:    -1000000000001,
			messageID: 1,
			want:      "https://t.me/c/1/1",
		},
		{
			name:      "negative id reports nothing",
			chatID:    -1001000000000,
			messageID: -5,
			want:      "",
		},
		{
			name:      "zero message id reports nothing",
			chatID:    -1001234567890,
			messageID: 0,
			want:      "",
		},
		{
			name:      "legacy basic group has no c-link",
			chatID:    -123456789,
			messageID: 7,
			want:      "",
		},
		{
			name:      "private chat has no c-link",
			chatID:    -100,
			messageID: 7,
			want:      "",
		},
		{
			name:      "positive chat id (a user) has no c-link",
			chatID:    1001234567890,
			messageID: 7,
			want:      "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := MessageLink(tc.chatID, tc.threadID, tc.messageID); got != tc.want {
				t.Fatalf("MessageLink(%d, %d, %d) = %q, want %q",
					tc.chatID, tc.threadID, tc.messageID, got, tc.want)
			}
		})
	}
}
