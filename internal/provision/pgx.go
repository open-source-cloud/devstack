package provision

import (
	"context"
	"errors"
	"fmt"
	"net/url"

	"github.com/jackc/pgx/v5"
)

// PgxConn is the pgx/v5-backed Conn used against a real shared Postgres. pgx is
// pure-Go, so it preserves the CGO-free static binary (DECISIONS D8).
type PgxConn struct {
	conn *pgx.Conn
}

// Connect dials Postgres at dsn (as the superuser) and returns a Conn plus a
// close func. The caller holds the flock around the provisioning it drives.
func Connect(ctx context.Context, dsn string) (*PgxConn, func() error, error) {
	c, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return nil, nil, fmt.Errorf("connect to shared postgres: %w", err)
	}
	return &PgxConn{conn: c}, func() error { return c.Close(ctx) }, nil
}

func (p *PgxConn) Exec(ctx context.Context, sql string, args ...any) error {
	_, err := p.conn.Exec(ctx, sql, args...)
	return err
}

func (p *PgxConn) Exists(ctx context.Context, sql string, args ...any) (bool, error) {
	var one int
	err := p.conn.QueryRow(ctx, sql, args...).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// DSN builds a libpq URL for connecting as a superuser to a shared Postgres
// reached at host:port. sslmode=disable is correct for the local shared network.
func DSN(host string, port int, user, password, database string) string {
	u := url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(user, password),
		Host:     fmt.Sprintf("%s:%d", host, port),
		Path:     "/" + database,
		RawQuery: "sslmode=disable",
	}
	return u.String()
}
