package lead

import (
	"context"
	"net/netip"

	"github.com/jackc/pgx/v5"

	"tenzo-run-backend/internal/shared/postgres"
)

const selectLeads = `
	select public_id, name, contact, locale, page, message, ip, created_at
	from leads`

type Repository struct {
	db postgres.DB
}

func NewRepository(db postgres.DB) *Repository {
	return &Repository{db: db}
}

func (r *Repository) insert(ctx context.Context, l Lead) error {
	const q = `
		insert into leads (public_id, name, contact, locale, page, message, ip, created_at)
		values ($1, $2, $3, $4, $5, $6, $7, $8)`

	_, err := r.db.Exec(ctx, q, l.ID, l.Name, l.Contact, string(l.Locale), l.Page, l.Message, ipArg(l.IP), l.CreatedAt)
	return err
}

func (r *Repository) latest(ctx context.Context, limit int) ([]Lead, error) {
	rows, err := r.db.Query(ctx, selectLeads+` order by created_at desc, id desc limit $1`, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, scan)
}

func scan(row pgx.CollectableRow) (Lead, error) {
	var (
		l      Lead
		locale string
		ip     *netip.Addr
	)
	if err := row.Scan(&l.ID, &l.Name, &l.Contact, &locale, &l.Page, &l.Message, &ip, &l.CreatedAt); err != nil {
		return Lead{}, err
	}
	l.Locale = Locale(locale)
	if ip != nil {
		l.IP = *ip
	}
	return l, nil
}

func ipArg(addr netip.Addr) *netip.Addr {
	if !addr.IsValid() {
		return nil
	}
	return &addr
}
