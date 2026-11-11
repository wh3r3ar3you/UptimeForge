//go:build !lite

package checks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	mssql "github.com/microsoft/go-mssqldb"
	go_ora "github.com/sijms/go-ora/v2"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

func init() {
	registerType(TypeInfo{
		Type: "sqlserver", Category: "database", Keyword: true,
		Fields: []Field{fHost, fPort("1433"), required(fUsername), fPassword, fDatabase, fQuery,
			{Name: "tls_mode", Kind: "select", Default: "require", Options: []string{"disable", "require", "verify"}, Advanced: true},
		},
		target: targetAddr,
	}, checkSQLServer)
	registerType(TypeInfo{
		Type: "oracledb", Category: "database", Keyword: true,
		Fields: []Field{fHost, fPort("1521"), required(fUsername), required(fPassword), required(fDatabase), fQuery},
		target: targetAddr,
	}, checkOracle)
	registerType(TypeInfo{
		Type: "mongodb", Category: "database", Keyword: true,
		Fields: []Field{fHost, fPort("27017"), fUsername, fPassword, fDatabase, fQuery,
			{Name: "auth_source", Kind: "text", Advanced: true}, fTLSMode},
		target: targetAddr,
		validate: func(p params) error {
			if p["query"] != "" && !json.Valid([]byte(p["query"])) {
				return errors.New("parameter query must be a JSON command, like {\"ping\":1}")
			}
			return nil
		},
	}, checkMongoDB)
}

func checkSQLServer(ctx context.Context, s *Spec, env *Env) Result {
	p := paramsOf(s)
	q := url.Values{}
	if p["database"] != "" {
		q.Set("database", p["database"])
	}
	switch p["tls_mode"] {
	case "disable":
		q.Set("encrypt", "disable")
	case "require":
		q.Set("encrypt", "true")
		q.Set("TrustServerCertificate", "true") // SQL Server generates a self-signed one by default
	case "verify":
		q.Set("encrypt", "true")
	}
	u := url.URL{Scheme: "sqlserver", User: url.UserPassword(p["username"], p["password"]), Host: p.addr(), RawQuery: q.Encode()}
	c, err := mssql.NewConnector(u.String())
	if err != nil {
		return fail("config", errors.New("bad SQL Server connection settings"))
	}
	c.Dialer = mssqlDialer{dialerFunc(env.Dial), p["host"]}
	return sqlCheck(ctx, c, p["query"], failSQLServer)
}

// mssqlDialer also names the host: without that the driver resolves it locally
// first, and through a proxy the name should be resolved at the far end.
type mssqlDialer struct {
	dialerFunc
	host string
}

func (d mssqlDialer) HostName() string { return d.host }

func failSQLServer(ctx context.Context, err error) Result {
	var me mssql.Error
	if errors.As(err, &me) {
		msg := fmt.Errorf("%d: %s", me.Number, me.Message)
		switch me.Number {
		case 18456, 18452, 18470, 18487, 18488:
			return fail("auth", msg)
		case 4060:
			return fail("not_found", msg)
		}
		return fail("query", msg)
	}
	if strings.Contains(err.Error(), "login error") {
		return fail("auth", err)
	}
	return fail(reasonOf(ctx, err), err)
}

func checkOracle(ctx context.Context, s *Spec, env *Env) Result {
	p := paramsOf(s)
	dsn := go_ora.BuildUrl(p["host"], p.int("port"), p["database"], p["username"], p["password"], nil)
	c := go_ora.NewConnector(dsn)
	oc, ok := c.(*go_ora.OracleConnector)
	if !ok {
		return fail("error", errors.New("unexpected go-ora connector"))
	}
	oc.Dialer(dialerFunc(env.Dial))
	query := p["query"]
	if query == "" {
		query = "SELECT 1 FROM DUAL"
	}
	return sqlCheck(ctx, oc, query, failOracle)
}

func failOracle(ctx context.Context, err error) Result {
	s := err.Error()
	switch {
	case strings.Contains(s, "ORA-01017") || strings.Contains(s, "ORA-28000") || strings.Contains(s, "ORA-01045"):
		return fail("auth", err)
	case strings.Contains(s, "ORA-12514") || strings.Contains(s, "ORA-12505"):
		return fail("not_found", err)
	case strings.Contains(s, "ORA-"):
		return fail("query", err)
	}
	return fail(reasonOf(ctx, err), err)
}

// checkMongoDB pings, then runs query as a database command. A command has no
// "first value" to speak of (a find's first field is the cursor), so Text is
// the whole reply as JSON.
func checkMongoDB(ctx context.Context, s *Spec, env *Env) Result {
	p := paramsOf(s)
	opts := options.Client().SetHosts([]string{p.addr()}).SetDirect(true).
		SetDialer(dialerFunc(env.Dial)).SetAppName("UptimeForge").SetRetryWrites(false).SetRetryReads(false)
	if deadline, ok := ctx.Deadline(); ok {
		// The driver keeps retrying a dead server until this runs out, and only
		// then says why. It has to run out before ctx does, or every cause reads as a timeout.
		left := time.Until(deadline)
		opts.SetServerSelectionTimeout(left * 2 / 3).SetConnectTimeout(left * 2 / 3)
	}
	if p["username"] != "" {
		opts.SetAuth(options.Credential{Username: p["username"], Password: p["password"], AuthSource: p["auth_source"]})
	}
	if cfg := tlsModeConfig(p, p["host"]); cfg != nil {
		opts.SetTLSConfig(cfg)
	}
	start := time.Now()
	client, err := mongo.Connect(opts)
	if err != nil {
		return failMongo(ctx, err)
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		client.Disconnect(closeCtx)
	}()
	if err := client.Ping(ctx, nil); err != nil {
		return failMongo(ctx, err)
	}
	r := Result{}
	if p["query"] != "" {
		var cmd bson.D
		if err := bson.UnmarshalExtJSON([]byte(p["query"]), false, &cmd); err != nil {
			return fail("config", errors.New("query is not a JSON command"))
		}
		db := p["database"]
		if db == "" {
			db = "admin"
		}
		raw, err := client.Database(db).RunCommand(ctx, cmd).Raw()
		if err != nil {
			var ce mongo.CommandError
			if errors.As(err, &ce) && !isMongoAuth(ce) {
				return fail("query", fmt.Errorf("%d: %s", ce.Code, ce.Message))
			}
			return failMongo(ctx, err)
		}
		text, err := bson.MarshalExtJSON(raw, false, false)
		if err != nil {
			return fail("bad_response", err)
		}
		r.Text = string(text)
	}
	r.LatencyMS = ms(time.Since(start))
	return r
}

func isMongoAuth(ce mongo.CommandError) bool { return ce.Code == 13 || ce.Code == 18 }

// failMongo looks into the text first: a selection error quotes the real cause
// of the last attempt, and reasonOf would call anything near the deadline a timeout.
func failMongo(ctx context.Context, err error) Result {
	var ce mongo.CommandError
	if errors.As(err, &ce) && isMongoAuth(ce) {
		return fail("auth", fmt.Errorf("%d: %s", ce.Code, ce.Message))
	}
	s := err.Error()
	switch {
	case strings.Contains(s, "Authentication failed") || strings.Contains(s, "auth error") || strings.Contains(s, "unable to authenticate"):
		return fail("auth", err)
	case strings.Contains(s, "connection refused") || strings.Contains(s, "actively refused"):
		return fail("refused", err)
	case strings.Contains(s, "no such host"):
		return fail("dns", err)
	case strings.Contains(s, "x509") || strings.Contains(s, "tls:"):
		return fail("tls", err)
	}
	if strings.Contains(s, "server selection") {
		return fail("timeout", err)
	}
	return fail(reasonOf(ctx, err), err)
}
