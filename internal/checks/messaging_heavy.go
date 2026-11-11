//go:build !lite

package checks

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/twmb/franz-go/pkg/kerr"
	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/pkg/sasl"
	"github.com/twmb/franz-go/pkg/sasl/plain"
	"github.com/twmb/franz-go/pkg/sasl/scram"
)

func init() {
	registerType(TypeInfo{
		Type: "mqtt", Category: "messaging", Keyword: true,
		Fields: []Field{fHost, fPort("1883"), {Name: "topic", Kind: "text", Required: true},
			fUsername, fPassword, fTLS, fInsecure},
		target: targetAddr,
	}, checkMQTT)
	registerType(TypeInfo{
		Type: "kafka-producer", Category: "messaging",
		Fields: []Field{
			{Name: "brokers", Kind: "textarea", Required: true},
			{Name: "topic", Kind: "text", Required: true},
			{Name: "message", Kind: "text", Default: "uptimeforge"},
			{Name: "sasl", Kind: "select", Default: "none", Options: []string{"none", "plain", "scram-sha-256", "scram-sha-512"}},
			fUsername, fPassword,
			{Name: "allow_auto_topic", Kind: "bool", Advanced: true},
			fTLS, fInsecure,
		},
		target: func(p map[string]string) string {
			if b := params(p).lines("brokers"); len(b) > 0 {
				return b[0]
			}
			return ""
		},
		validate: func(p params) error {
			for _, b := range p.lines("brokers") {
				if _, _, err := net.SplitHostPort(b); err != nil || strings.ContainsAny(b, "/@") {
					return errors.New("parameter brokers must list host:port entries, credentials go elsewhere")
				}
			}
			return nil
		},
	}, checkKafka)
}

// tlsDial wraps a probe connection in TLS when the check asks for it.
func tlsDial(ctx context.Context, env *Env, p params, addr string) (net.Conn, error) {
	conn, err := env.Dial(ctx, "tcp", addr)
	if err != nil || !p.bool("tls") {
		return conn, err
	}
	host, _, _ := net.SplitHostPort(addr)
	tc := tls.Client(conn, clientTLS(p, host))
	if err := tc.HandshakeContext(ctx); err != nil {
		conn.Close()
		return nil, err
	}
	return tc, nil
}

// checkMQTT subscribes and waits for one message, which proves the broker
// routes, not only that it accepts logins. A topic nothing publishes to is
// reason no_message, not a broker failure.
func checkMQTT(ctx context.Context, s *Spec, env *Env) Result {
	p := paramsOf(s)
	opts := mqtt.NewClientOptions().AddBroker("tcp://" + p.addr()).
		SetClientID("uptimeforge-" + sipToken()).
		SetUsername(p["username"]).SetPassword(p["password"]).
		SetAutoReconnect(false).SetConnectRetry(false).SetCleanSession(true).
		SetConnectTimeout(DefaultTimeout).
		SetCustomOpenConnectionFn(func(_ *url.URL, _ mqtt.ClientOptions) (net.Conn, error) {
			return tlsDial(ctx, env, p, p.addr())
		})
	if deadline, ok := ctx.Deadline(); ok {
		opts.SetConnectTimeout(time.Until(deadline))
	}
	start := time.Now()
	client := mqtt.NewClient(opts)
	defer client.Disconnect(50)
	if res := mqttWait(ctx, client.Connect()); res != nil {
		return *res
	}
	connected := ms(time.Since(start))
	got := make(chan string, 1)
	sub := client.Subscribe(p["topic"], 0, func(_ mqtt.Client, m mqtt.Message) {
		select {
		case got <- string(m.Payload()):
		default:
		}
	})
	if res := mqttWait(ctx, sub); res != nil {
		return *res
	}
	select {
	case payload := <-got:
		return Result{LatencyMS: connected, Text: payload,
			Phases: map[string]float64{"connect": connected, "message": ms(time.Since(start))}}
	case <-ctx.Done():
		return Result{Status: Down, Reason: "no_message", LatencyMS: connected,
			Error: fmt.Sprintf("broker works, but no message on %s in time", p["topic"])}
	}
}

func mqttWait(ctx context.Context, t mqtt.Token) *Result {
	select {
	case <-t.Done():
	case <-ctx.Done():
		r := fail("timeout", errors.New("broker did not answer in time"))
		return &r
	}
	err := t.Error()
	if err == nil {
		return nil
	}
	reason := reasonOf(ctx, err)
	s := strings.ToLower(err.Error())
	if strings.Contains(s, "bad user name or password") || strings.Contains(s, "not authorized") {
		reason = "auth"
	}
	r := fail(reason, err)
	return &r
}

// checkKafka produces one record and waits for the broker's acknowledgement.
func checkKafka(ctx context.Context, s *Spec, env *Env) Result {
	p := paramsOf(s)
	opts := []kgo.Opt{
		kgo.SeedBrokers(p.lines("brokers")...),
		kgo.DefaultProduceTopic(p["topic"]),
		kgo.ClientID("uptimeforge"),
		kgo.Dialer(func(ctx context.Context, _, host string) (net.Conn, error) {
			return tlsDial(ctx, env, p, host)
		}),
		kgo.RecordRetries(1),
		kgo.RequestRetries(1),
		kgo.UnknownTopicRetries(1),
	}
	if p.bool("allow_auto_topic") {
		opts = append(opts, kgo.AllowAutoTopicCreation())
	}
	var mech sasl.Mechanism
	auth := scram.Auth{User: p["username"], Pass: p["password"]}
	switch p["sasl"] {
	case "plain":
		mech = plain.Auth{User: p["username"], Pass: p["password"]}.AsMechanism()
	case "scram-sha-256":
		mech = auth.AsSha256Mechanism()
	case "scram-sha-512":
		mech = auth.AsSha512Mechanism()
	}
	if mech != nil {
		opts = append(opts, kgo.SASL(mech))
	}
	client, err := kgo.NewClient(opts...)
	if err != nil {
		return fail("config", err)
	}
	defer client.Close()
	start := time.Now()
	// Produce retries a refused login until ctx ends, which would read as a
	// timeout. Ping reports it at once.
	if err := client.Ping(ctx); err != nil {
		return failKafka(ctx, err)
	}
	if err := client.ProduceSync(ctx, &kgo.Record{Value: []byte(p["message"])}).FirstErr(); err != nil {
		return failKafka(ctx, err)
	}
	return Result{LatencyMS: ms(time.Since(start))}
}

func failKafka(ctx context.Context, err error) Result {
	switch {
	case errors.Is(err, kerr.SaslAuthenticationFailed), errors.Is(err, kerr.TopicAuthorizationFailed),
		errors.Is(err, kerr.ClusterAuthorizationFailed), errors.Is(err, kerr.IllegalSaslState):
		return fail("auth", err)
	case errors.Is(err, kerr.UnknownTopicOrPartition):
		return fail("not_found", err)
	}
	var ke *kerr.Error
	if errors.As(err, &ke) {
		return fail("bad_response", err)
	}
	return fail(reasonOf(ctx, err), err)
}
