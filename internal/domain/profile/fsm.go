package profile

import (
	"context"
	"sync"
	"time"
)

type Step int

const (
	StepStack Step = iota
	StepExperience
	StepBio
	StepConfirm
)

type Mode int

const (
	ModeRegister Mode = iota
	ModeUpdate
)

type Session struct {
	ChatID     int64
	Step       Step
	Mode       Mode
	Stack      string
	Experience string
	Bio        string
	LastTouch  time.Time
}

type FSMStore struct {
	mu       sync.RWMutex
	sessions map[int64]*Session
}

func NewFSMStore() *FSMStore {
	return &FSMStore{
		sessions: make(map[int64]*Session),
	}
}

func (f *FSMStore) Get(userID int64) (*Session, bool) {
	f.mu.RLock()
	defer f.mu.RUnlock()

	s, ok := f.sessions[userID]
	if !ok {
		return nil, false
	}

	if time.Since(s.LastTouch) > time.Hour {
		return nil, false
	}

	cp := *s
	return &cp, true
}

func (f *FSMStore) Set(userID int64, s *Session) {
	f.mu.Lock()
	defer f.mu.Unlock()

	s.LastTouch = time.Now()
	f.sessions[userID] = s
}

func (f *FSMStore) Delete(userID int64) {
	f.mu.Lock()
	defer f.mu.Unlock()

	delete(f.sessions, userID)
}

func (f *FSMStore) Has(userID int64) bool {
	f.mu.RLock()
	defer f.mu.RUnlock()

	s, ok := f.sessions[userID]
	if !ok {
		return false
	}

	if time.Since(s.LastTouch) > time.Hour {
		return false
	}

	return true
}

func (f *FSMStore) RunSweeper(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			f.mu.Lock()
			now := time.Now()
			for userID, s := range f.sessions {
				if now.Sub(s.LastTouch) > time.Hour {
					delete(f.sessions, userID)
				}
			}
			f.mu.Unlock()
		}
	}
}
