package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"tenzo-run-backend/internal/shared/postgres"
)

const (
	sessionColumns = `
	id, public_id, channel, coalesce(telegram_id, 0), coalesce(profile, ''), locale::text, coalesce(page, ''),
	coalesce(name, ''), coalesce(contact, ''), coalesce(project, ''), coalesce(budget, ''), coalesce(deadline, ''),
	turns, waiting, coalesce(question, ''), created_at, updated_at`
	selectSessions = `select ` + sessionColumns + ` from chat_sessions`
)

type Repository struct {
	db postgres.DB
}

func NewRepository(db postgres.DB) *Repository {
	return &Repository{db: db}
}

func scanSession(row pgx.Row) (Session, error) {
	var (
		s     Session
		turns []byte
	)
	err := row.Scan(&s.ID, &s.PublicID, &s.Channel, &s.TelegramID, &s.Profile, &s.Locale, &s.Page,
		&s.Lead.Name, &s.Lead.Contact, &s.Lead.Project, &s.Lead.Budget, &s.Lead.Deadline,
		&turns, &s.Waiting, &s.Question, &s.CreatedAt, &s.UpdatedAt)
	if err != nil {
		return Session{}, err
	}
	if err := json.Unmarshal(turns, &s.Turns); err != nil {
		return Session{}, fmt.Errorf("decode turns: %w", err)
	}
	return s, nil
}

func (r *Repository) find(ctx context.Context, id uuid.UUID) (Session, error) {
	return scanSession(r.db.QueryRow(ctx, selectSessions+` where public_id = $1`, id))
}

func (r *Repository) byID(ctx context.Context, id int64) (Session, error) {
	return scanSession(r.db.QueryRow(ctx, selectSessions+` where id = $1`, id))
}

func (r *Repository) byTelegram(ctx context.Context, telegramID int64) (Session, error) {
	return scanSession(r.db.QueryRow(ctx, selectSessions+` where telegram_id = $1 order by id desc limit 1`, telegramID))
}

func (r *Repository) create(ctx context.Context, s *Session) error {
	turns, err := json.Marshal(s.turnsOrEmpty())
	if err != nil {
		return fmt.Errorf("encode turns: %w", err)
	}

	const q = `
		insert into chat_sessions (public_id, channel, telegram_id, profile, locale, page, name, contact, project,
		                           budget, deadline, turns, waiting, question, created_at, updated_at)
		values ($1, $2, nullif($3::bigint, 0), nullif($4, ''), $5, nullif($6, ''), nullif($7, ''), nullif($8, ''),
		        nullif($9, ''), nullif($10, ''), nullif($11, ''), $12, $13, nullif($14, ''), $15, $16)
		returning id`

	return r.db.QueryRow(ctx, q, s.PublicID, s.Channel, s.TelegramID, s.Profile, s.Locale, s.Page, s.Lead.Name,
		s.Lead.Contact, s.Lead.Project, s.Lead.Budget, s.Lead.Deadline, turns, s.Waiting, s.Question,
		s.CreatedAt, s.UpdatedAt).Scan(&s.ID)
}

func (r *Repository) save(ctx context.Context, s *Session) error {
	turns, err := json.Marshal(s.turnsOrEmpty())
	if err != nil {
		return fmt.Errorf("encode turns: %w", err)
	}

	const q = `
		update chat_sessions
		set profile = nullif($2, ''), name = nullif($3, ''), contact = nullif($4, ''), project = nullif($5, ''),
		    budget = nullif($6, ''), deadline = nullif($7, ''), turns = $8, waiting = $9, question = nullif($10, ''),
		    updated_at = $11
		where id = $1`

	_, err = r.db.Exec(ctx, q, s.ID, s.Profile, s.Lead.Name, s.Lead.Contact, s.Lead.Project, s.Lead.Budget,
		s.Lead.Deadline, turns, s.Waiting, s.Question, s.UpdatedAt)
	return err
}

func (s *Session) turnsOrEmpty() []Turn {
	if s.Turns == nil {
		return []Turn{}
	}
	return s.Turns
}

func (r *Repository) list(ctx context.Context, f Filter) ([]Session, error) {
	q := selectSessions + ` where jsonb_array_length(turns) > 0`
	args := []any{}
	if f.Query != "" {
		args = append(args, "%"+escapeLike(f.Query)+"%")
		q += ` and (name ilike $1 or contact ilike $1 or project ilike $1 or profile ilike $1)`
	}
	args = append(args, f.Limit)
	q += fmt.Sprintf(` order by updated_at desc, id desc limit $%d`, len(args))

	rows, err := r.db.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []Session
	for rows.Next() {
		s, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, s)
	}
	return items, rows.Err()
}

func (r *Repository) stats(ctx context.Context, since time.Time) (Stats, error) {
	var st Stats
	err := r.db.QueryRow(ctx, `
		select count(*) filter (where updated_at >= $1 and jsonb_array_length(turns) > 0),
		       count(*) filter (where updated_at >= $1 and contact is not null),
		       count(*) filter (where waiting)
		from chat_sessions`, since).Scan(&st.Chats, &st.Contacts, &st.Waiting)
	return st, err
}

func (r *Repository) purge(ctx context.Context, before time.Time) (int64, error) {
	tag, err := r.db.Exec(ctx, `delete from chat_sessions where updated_at < $1 and contact is null`, before)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}
