package postgres

import (
	"errors"

	"github.com/jackc/pgx/v5"
)

var ErrNoRows = pgx.ErrNoRows

func IsNoRows(err error) bool {
	return errors.Is(err, pgx.ErrNoRows)
}
