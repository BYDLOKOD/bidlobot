package main

import (
	"log/slog"
	"math/rand"
	"time"

	"go.etcd.io/bbolt"

	"github.com/veschin/bidlobot/internal/bot"
	"github.com/veschin/bidlobot/internal/games/battle"
	"github.com/veschin/bidlobot/internal/games/dice"
	"github.com/veschin/bidlobot/internal/games/guess"
	"github.com/veschin/bidlobot/internal/games/hangman"
	"github.com/veschin/bidlobot/internal/games/quiz"
	"github.com/veschin/bidlobot/internal/shared/tgclient"
	"github.com/veschin/bidlobot/internal/storage"
)

// buildGames constructs the GamesRegistry. It takes the concrete
// rate-limited *tgclient.Client (not bot.GamesSender) because the newer
// games need methods outside that narrow interface - notably SendPoll -
// and every send must still go through the per-chat rate budget. Each
// handler constructor takes its own narrow sender interface, all of which
// *tgclient.Client satisfies. botUsername (from GetMe) lets /duel reject
// a duel against the bot itself; "" disables that guard.
func buildGames(db *bbolt.DB, sender *tgclient.Client, botUsername string, log *slog.Logger) *bot.GamesRegistry {
	diceRepo := storage.NewDiceRepo(db)
	diceSvc := dice.NewService(diceRepo, log)
	diceHandler := bot.NewDiceHandler(diceSvc, sender, log)

	battleRegistry := battle.NewRegistry()
	battleHandler := bot.NewBattleHandler(battleRegistry, sender, log)

	quizRepo := storage.NewQuizRepo(db)
	quizActive := quiz.NewActiveQuizzes()
	quizHandler := bot.NewQuizHandler(quizActive, quizRepo, sender, log)

	// Phase 5 mini-games.
	pollHandler := bot.NewPollHandler(sender, log)
	eightBallHandler := bot.NewEightBallHandler(sender, log)
	quipHandler := bot.NewQuipHandler(sender, log)

	guessRepo := storage.NewGuessRepo(db)
	guessSvc := guess.NewService(guessRepo, rand.New(rand.NewSource(time.Now().UnixNano())), log)
	guessHandler := bot.NewGuessHandler(guessSvc, sender, log)

	hangmanRepo := storage.NewHangmanRepo(db)
	hangmanSvc := hangman.NewService(hangmanRepo, rand.New(rand.NewSource(time.Now().UnixNano())), log)
	hangmanHandler := bot.NewHangmanHandler(hangmanSvc, sender, log)

	// /duel must only target members the bot has observed in this chat
	// (membership repo is a stateless *bbolt.DB wrapper like the others
	// built here).
	duelMembers := storage.NewMembershipRepo(db)
	duelHandler := bot.NewDuelHandler(sender, duelMembers, botUsername, log)

	// Trivia reuses the same per-chat quiz leaderboard store as /quiz.
	triviaHandler := bot.NewTriviaHandler(quizRepo, sender, log)

	return &bot.GamesRegistry{
		Dice: diceHandler,
		Battle: bot.BattleRoutes{
			Slash:            battleHandler.HandleBattle,
			ReactionObserver: battleHandler.ObserveReaction,
		},
		Quiz: bot.QuizRoutes{
			Slash:             quizHandler.HandleQuiz,
			Callback:          quizHandler.HandleCallback,
			CallbackPredicate: bot.QuizCallbackPredicate(),
		},
		InlineRouter: bot.NewGamesInlineRouter(),

		Poll:      pollHandler,
		EightBall: eightBallHandler,
		Quip:      quipHandler,
		Guess:     guessHandler.HandleGuess,
		Hangman:   hangmanHandler.HandleHangman,
		Duel:      duelHandler.HandleDuel,
		Trivia: bot.TriviaRoutes{
			Slash:             triviaHandler.HandleTrivia,
			Callback:          triviaHandler.HandleCallback,
			CallbackPredicate: bot.TriviaCallbackPredicate(),
		},
	}
}
