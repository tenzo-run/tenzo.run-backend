package admin

import (
	"context"
	"time"

	"tenzo-run-backend/internal/chat"
	"tenzo-run-backend/internal/shared/postgres"
)

const updatesRetention = 7 * 24 * time.Hour

type Repository struct {
	db postgres.DB
}

func NewRepository(db postgres.DB) *Repository {
	return &Repository{db: db}
}

type knownAdmin struct {
	ID       int64
	Username string
}

func (r *Repository) remember(ctx context.Context, sessionID int64, posted []chat.Posted, at time.Time) error {
	for _, p := range posted {
		if _, err := r.db.Exec(ctx, `
			insert into bot_messages (chat_id, message_id, session_id, created_at) values ($1, $2, $3, $4)
			on conflict (chat_id, message_id) do update set session_id = excluded.session_id`,
			p.ChatID, p.MessageID, sessionID, at); err != nil {
			return err
		}
	}
	return nil
}

func (r *Repository) sessionOf(ctx context.Context, chatID, messageID int64) (int64, error) {
	var id int64
	err := r.db.QueryRow(ctx, `select session_id from bot_messages where chat_id = $1 and message_id = $2`,
		chatID, messageID).Scan(&id)
	return id, err
}

func (r *Repository) latestMessage(ctx context.Context, chatID, sessionID int64) (int64, error) {
	var id int64
	err := r.db.QueryRow(ctx, `
		select message_id from bot_messages where chat_id = $1 and session_id = $2
		order by message_id desc limit 1`, chatID, sessionID).Scan(&id)
	if postgres.IsNoRows(err) {
		return 0, nil
	}
	return id, err
}

func (r *Repository) claimUpdate(ctx context.Context, id int64, at time.Time) (bool, error) {
	tag, err := r.db.Exec(ctx, `insert into bot_updates (update_id, handled_at) values ($1, $2) on conflict do nothing`, id, at)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func (r *Repository) lastUpdate(ctx context.Context) (int64, error) {
	var id int64
	err := r.db.QueryRow(ctx, `select coalesce(max(update_id), 0) from bot_updates`).Scan(&id)
	return id, err
}

func (r *Repository) purgeUpdates(ctx context.Context, before time.Time) error {
	_, err := r.db.Exec(ctx, `delete from bot_updates where handled_at < $1`, before)
	return err
}

func (r *Repository) admins(ctx context.Context) ([]knownAdmin, error) {
	rows, err := r.db.Query(ctx, `select user_id, username from bot_admins order by created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []knownAdmin
	for rows.Next() {
		var a knownAdmin
		if err := rows.Scan(&a.ID, &a.Username); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (r *Repository) saveAdmin(ctx context.Context, id int64, username, name string, at time.Time) error {
	_, err := r.db.Exec(ctx, `
		insert into bot_admins (user_id, username, name, created_at) values ($1, $2, $3, $4)
		on conflict (user_id) do update set username = excluded.username, name = excluded.name`,
		id, username, name, at)
	return err
}

func (r *Repository) dropAdmin(ctx context.Context, id int64) error {
	_, err := r.db.Exec(ctx, `delete from bot_admins where user_id = $1`, id)
	return err
}
