package admin

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"tenzo-run-backend/internal/shared/telegram"
)

type Admins struct {
	repo      *Repository
	ids       []int64
	usernames map[string]bool
	now       func() time.Time

	mu     sync.Mutex
	known  map[int64]string
	loaded bool
}

func NewAdmins(repo *Repository, chatIDs, usernames []string) *Admins {
	a := &Admins{repo: repo, usernames: map[string]bool{}, known: map[int64]string{}, now: time.Now}
	for _, id := range chatIDs {
		if n, err := strconv.ParseInt(strings.TrimSpace(id), 10, 64); err == nil && n > 0 && !slices.Contains(a.ids, n) {
			a.ids = append(a.ids, n)
		}
	}
	for _, u := range usernames {
		if u = normalizeUsername(u); u != "" {
			a.usernames[u] = true
		}
	}
	return a
}

func normalizeUsername(u string) string {
	return strings.ToLower(strings.TrimPrefix(strings.TrimSpace(u), "@"))
}

func (a *Admins) Allowed(u telegram.User) bool {
	return slices.Contains(a.ids, u.ID) || (u.Username != "" && a.usernames[normalizeUsername(u.Username)])
}

func (a *Admins) Seen(ctx context.Context, u telegram.User) (bool, error) {
	if err := a.load(ctx); err != nil {
		return false, err
	}
	allowed := a.Allowed(u)
	username := normalizeUsername(u.Username)

	a.mu.Lock()
	stored, ok := a.known[u.ID]
	a.mu.Unlock()

	switch {
	case allowed && (!ok || stored != username):
		if err := a.repo.saveAdmin(ctx, u.ID, username, u.Name(), a.now().UTC()); err != nil {
			return allowed, fmt.Errorf("save admin: %w", err)
		}
		a.mu.Lock()
		a.known[u.ID] = username
		a.mu.Unlock()
		return allowed, nil
	case !allowed && ok:
		if err := a.repo.dropAdmin(ctx, u.ID); err != nil {
			return allowed, fmt.Errorf("drop admin: %w", err)
		}
		a.mu.Lock()
		delete(a.known, u.ID)
		a.mu.Unlock()
	}
	return allowed, nil
}

func (a *Admins) IDs(ctx context.Context) []int64 {
	_ = a.load(ctx)
	out := slices.Clone(a.ids)
	a.mu.Lock()
	defer a.mu.Unlock()
	for id, username := range a.known {
		if a.usernames[username] && !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	slices.Sort(out)
	return out
}

func (a *Admins) load(ctx context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.loaded {
		return nil
	}
	list, err := a.repo.admins(ctx)
	if err != nil {
		return fmt.Errorf("load admins: %w", err)
	}
	for _, k := range list {
		a.known[k.ID] = k.Username
	}
	a.loaded = true
	return nil
}
