package checks

import (
	"bufio"
	"context"
	"crypto/tls"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func init() {
	registerType(TypeInfo{
		Type: "postgres", Category: "database", Keyword: true,
		Fields: []Field{fHost, fPort("5432"), required(fUsername), fPassword, fDatabase, fQuery,
			{Name: "tls_mode", Kind: "select", Default: "prefer", Options: []string{"prefer", "disable", "require", "verify"}, Advanced: true},
		},
		target: targetAddr,
	}, checkPostgres)
	registerType(TypeInfo{
		Type: "mysql", Category: "database", Keyword: true,
		Fields: []Field{fHost, fPort("3306"), required(fUsername), fPassword, fDatabase, fQuery, fTLSMode},
		target: targetAddr,
	}, checkMySQL)
	registerType(TypeInfo{
		Type: "redis", Category: "database",
		Fields: []Field{fHost, fPort("6379"), fUsername, fPassword, fTLS, fInsecure},
		target: targetAddr,
	}, checkRedis)
}

// tlsModeConfig reads the shared tls_mode field. nil means no TLS.
func tlsModeConfig(p params, host string) *tls.Config {
	switch p["tls_mode"] {
	case "require", "prefer":
		// Encrypt without trusting: database certificates are mostly self-signed.
		return &tls.Config{ServerName: host, InsecureSkipVerify: true}
	case "verify":
		return &tls.Config{ServerName: host}
	}
	return nil
}

func checkPostgres(ctx context.Context, s *Spec, env *Env) Result {
	p := paramsOf(s)
	cfg, err := pgx.ParseConfig("sslmode=disable")
	if err != nil {
		return fail("config", err)
	}
	cfg.Host, cfg.Port = p["host"], uint16(p.int("port"))
	cfg.User, cfg.Password, cfg.Database = p["username"], p["password"], p["database"]
	cfg.TLSConfig = tlsModeConfig(p, p["host"])
	cfg.Fallbacks = nil
	if p["tls_mode"] == "prefer" {
		cfg.Fallbacks = []*pgconn.FallbackConfig{{Host: cfg.Host, Port: cfg.Port}}
	}
	cfg.DialFunc = pgconn.DialFunc(env.Dial)
	// The probe's dialer resolves names; through a proxy that is the far end.
	cfg.LookupFunc = func(_ context.Context, host string) ([]string, error) { return []string{host}, nil }
	cfg.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol

	start := time.Now()
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return failPostgres(ctx, err)
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		conn.Close(closeCtx)
	}()
	query := p["query"]
	if query == "" {
		if err := conn.Ping(ctx); err != nil {
			return failPostgres(ctx, err)
		}
		return Result{LatencyMS: ms(time.Since(start))}
	}
	rows, err := conn.Query(ctx, query)
	if err != nil {
		return failPostgres(ctx, err)
	}
	defer rows.Close()
	r := Result{}
	if rows.Next() {
		values, err := rows.Values()
		if err != nil {
			return failPostgres(ctx, err)
		}
		if len(values) > 0 {
			r.Text = fmt.Sprint(values[0])
		}
	}
	if err := rows.Err(); err != nil {
		return failPostgres(ctx, err)
	}
	r.LatencyMS = ms(time.Since(start))
	return r
}

func failPostgres(ctx context.Context, err error) Result {
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		msg := fmt.Errorf("%s: %s", pe.Code, pe.Message)
		switch {
		case pe.Code == "28P01" || pe.Code == "28000" || strings.HasPrefix(pe.Code, "28"):
			return fail("auth", msg)
		case pe.Code == "3D000":
			return fail("not_found", msg)
		}
		return fail("query", msg)
	}
	return fail(reasonOf(ctx, err), err)
}

func checkMySQL(ctx context.Context, s *Spec, env *Env) Result {
	p := paramsOf(s)
	cfg := mysql.NewConfig()
	cfg.User, cfg.Passwd, cfg.Net, cfg.Addr, cfg.DBName = p["username"], p["password"], "tcp", p.addr(), p["database"]
	cfg.DialFunc = env.Dial
	cfg.TLS = tlsModeConfig(p, p["host"])
	cfg.Logger = mysql.Logger(discardLogger{}) // its log lines would bypass scrubbing
	connector, err := mysql.NewConnector(cfg)
	if err != nil {
		return fail("config", err)
	}
	return sqlCheck(ctx, connector, p["query"], failMySQL)
}

type discardLogger struct{}

func (discardLogger) Print(...any) {}

func failMySQL(ctx context.Context, err error) Result {
	var me *mysql.MySQLError
	if errors.As(err, &me) {
		msg := fmt.Errorf("%d: %s", me.Number, me.Message)
		switch me.Number {
		case 1045, 1044, 1698, 1227:
			return fail("auth", msg)
		case 1049:
			return fail("not_found", msg)
		}
		return fail("query", msg)
	}
	return fail(reasonOf(ctx, err), err)
}

// sqlCheck runs the shared part of database/sql drivers: connect, then the
// query if there is one, whose first column of the first row becomes Text.
func sqlCheck(ctx context.Context, c driver.Connector, query string, failure func(context.Context, error) Result) Result {
	db := sql.OpenDB(c)
	defer db.Close()
	db.SetMaxOpenConns(1)
	start := time.Now()
	if query == "" {
		if err := db.PingContext(ctx); err != nil {
			return failure(ctx, err)
		}
		return Result{LatencyMS: ms(time.Since(start))}
	}
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return failure(ctx, err)
	}
	defer rows.Close()
	r := Result{}
	if rows.Next() {
		cols, err := rows.Columns()
		if err != nil {
			return failure(ctx, err)
		}
		values := make([]any, len(cols))
		for i := range values {
			values[i] = new(sql.RawBytes)
		}
		if err := rows.Scan(values...); err != nil {
			return failure(ctx, err)
		}
		if len(values) > 0 {
			r.Text = string(*values[0].(*sql.RawBytes))
		}
	}
	if err := rows.Err(); err != nil {
		return failure(ctx, err)
	}
	r.LatencyMS = ms(time.Since(start))
	return r
}

// dialerFunc adapts Env.Dial to the DialContext interface some drivers want.
type dialerFunc func(ctx context.Context, network, addr string) (net.Conn, error)

func (f dialerFunc) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	return f(ctx, network, addr)
}

// checkRedis speaks just enough RESP to log in and ping, which a port-open
// check cannot tell from a Redis that refuses every client.
func checkRedis(ctx context.Context, s *Spec, env *Env) Result {
	p := paramsOf(s)
	start := time.Now()
	raw, err := env.Dial(ctx, "tcp", p.addr())
	if err != nil {
		return fail(reasonOf(ctx, err), err)
	}
	var conn io.ReadWriteCloser = raw
	defer func() { conn.Close() }()
	stop := closeOnDone(ctx, raw)
	defer stop()
	if p.bool("tls") {
		tc := tls.Client(raw, clientTLS(p, p["host"]))
		if err := tc.HandshakeContext(ctx); err != nil {
			return fail(reasonOf(ctx, err), err)
		}
		conn = tc
	}
	br := bufio.NewReader(&capReader{conn, 64 << 10}) // a few status lines
	if p["password"] != "" {
		args := []string{"AUTH", p["password"]}
		if p["username"] != "" {
			args = []string{"AUTH", p["username"], p["password"]}
		}
		reply, err := redisCall(conn, br, args...)
		if err != nil {
			return failRedisIO(ctx, err)
		}
		if strings.HasPrefix(reply, "-") {
			return failRedis(reply)
		}
	}
	reply, err := redisCall(conn, br, "PING")
	if err != nil {
		return failRedisIO(ctx, err)
	}
	if strings.HasPrefix(reply, "-") {
		return failRedis(reply)
	}
	if reply != "+PONG" {
		return fail("bad_response", fmt.Errorf("answered %q to PING", clip(reply, 60)))
	}
	conn.Write([]byte("*1\r\n$4\r\nQUIT\r\n"))
	return Result{LatencyMS: ms(time.Since(start))}
}

func failRedisIO(ctx context.Context, err error) Result {
	if errors.Is(err, errTooMuch) {
		return fail("bad_response", errors.New("not a Redis answer: no end of line in 64 KB"))
	}
	return fail(reasonOf(ctx, err), err)
}

// failRedis keeps error replies short and never echoes our command. -LOADING,
// -MASTERDOWN and friends are the server's own state, so they keep their word.
func failRedis(reply string) Result {
	text := clip(strings.TrimPrefix(reply, "-"), 200)
	word, _, _ := strings.Cut(text, " ")
	switch word {
	case "NOAUTH", "WRONGPASS", "NOPERM":
		return fail("auth", errors.New(text))
	case "ERR":
		if strings.Contains(text, "AUTH") || strings.Contains(strings.ToLower(text), "password") {
			return fail("auth", errors.New(text))
		}
	}
	return fail("bad_response", errors.New(text))
}

func redisCall(w io.Writer, br *bufio.Reader, args ...string) (string, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "*%d\r\n", len(args))
	for _, a := range args {
		fmt.Fprintf(&b, "$%d\r\n%s\r\n", len(a), a)
	}
	if _, err := io.WriteString(w, b.String()); err != nil {
		return "", err
	}
	line, err := br.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}
