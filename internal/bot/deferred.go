package bot

// Per-user deferred-job flush and TTL sweep.
//
// /flush is a supergroup command: any user can flush their own queue.
// The flush worker iterates the caller's deferred jobs and dispatches by
// type (tiktok, summarize). Successful jobs are removed from the queue;
// failures stay for the next flush. Entries older than 48h are swept by
// a periodic GC loop.

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"

	"github.com/veschin/bidlobot/internal/storage"
)

// flushCooldown prevents /flush spam.
const flushCooldown = 30 * time.Second

// handleFlush is the public supergroup /flush command. Processes only the
// calling user's deferred jobs.
func (a *App) handleFlush(_ *th.Context, msg telego.Message) error {
	if msg.From == nil {
		return nil
	}
	if a.deferredQ == nil {
		return nil
	}

	ctx := context.Background()
	jobs, err := a.deferredQ.ListByUser(ctx, msg.From.ID)
	if err != nil {
		a.log.Warn("flush: list failed", "error", err)
		return nil
	}
	if len(jobs) == 0 {
		a.sender.SendMessage(ctx, &telego.SendMessageParams{
			ChatID: telego.ChatID{ID: msg.Chat.ID},
			Text:   "Очередь пуста.",
		})
		return nil
	}

	a.sender.SendMessage(ctx, &telego.SendMessageParams{
		ChatID: telego.ChatID{ID: msg.Chat.ID},
		Text:   fmt.Sprintf("Выгружаю %d...", len(jobs)),
	})

	go a.flushDeferredJobs(msg.From.ID, msg.Chat.ID, jobs)
	return nil
}

// flushDeferredJobs processes jobs sequentially and reports the outcome.
func (a *App) flushDeferredJobs(userID int64, reportChatID int64, jobs []storage.DeferredJob) {
	ctx := context.Background()
	snd := a.sanitizerSender()
	succeeded, failed := 0, 0
	for _, job := range jobs {
		var err error
		switch job.Type {
		case storage.DeferredTikTok:
			var p storage.TikTokPayload
			if jErr := json.Unmarshal(job.Payload, &p); jErr != nil {
				err = fmt.Errorf("decode payload: %w", jErr)
			} else {
				err = tryTikTokExport(ctx, snd, a.log, a.repReactor,
					job.ChatID, job.MessageID, job.UserID, p.URL, p.Username, p.FirstName, p.Caption)
			}
		case storage.DeferredSummarize:
			err = a.retrySummarize(ctx, job)
		default:
			continue
		}
		if err != nil {
			a.log.Warn("flush: job failed, keeping in queue",
				"type", job.Type, "user_id", userID, "error", err)
			failed++
		} else {
			a.deferredQ.Delete(ctx, job.Key)
			succeeded++
		}
	}

	text := fmt.Sprintf("Очередь: обработано %d, успешно %d, осталось %d.",
		len(jobs), succeeded, failed)
	a.sender.SendMessage(ctx, &telego.SendMessageParams{
		ChatID: telego.ChatID{ID: reportChatID},
		Text:   text,
	})
}

// retrySummarize re-runs a failed summarize request and edits the
// original placeholder with the result.
func (a *App) retrySummarize(ctx context.Context, job storage.DeferredJob) error {
	var p storage.SummarizePayload
	if err := json.Unmarshal(job.Payload, &p); err != nil {
		return fmt.Errorf("decode payload: %w", err)
	}

	absChatID := storage.AbsChatID(job.ChatID)
	body, meta, serr := a.summarize.Summarize(absChatID, p.N, p.Questions)
	if serr != nil {
		return fmt.Errorf("summarize: %w", serr)
	}

	final := composeSummaryMessage(body, meta, p.Requester, nil)
	ectx, ecancel := context.WithTimeout(ctx, summarizeEditTO)
	defer ecancel()
	if _, eerr := a.summarizeSender.EditMessageText(ectx, &telego.EditMessageTextParams{
		ChatID:    telego.ChatID{ID: job.ChatID},
		MessageID: p.PlaceholderID,
		Text:      final,
	}); eerr != nil {
		return fmt.Errorf("edit placeholder: %w", eerr)
	}
	return nil
}

// runDeferredGC sweeps expired deferred jobs every interval. Entries
// older than storage.DeferredTTL are removed.
func (a *App) runDeferredGC(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			n, err := a.deferredQ.GarbageCollect(ctx, time.Now().UTC().Add(-storage.DeferredTTL))
			if err != nil {
				a.log.Warn("deferred GC failed", "error", err)
				continue
			}
			if n > 0 {
				a.log.Info("deferred GC removed expired jobs", "count", n)
			}
		}
	}
}
