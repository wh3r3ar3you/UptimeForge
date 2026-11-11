package checks

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

func init() {
	registerType(TypeInfo{
		Type: "gamedig", Category: "game",
		// Unlike Kuma's list of hundreds, only these protocols exist here.
		Fields: []Field{
			{Name: "game", Kind: "select", Required: true, Options: []string{"source", "minecraft", "minecraft-bedrock"}},
			fHost, {Name: "port", Kind: "number", Required: true},
		},
		target: targetAddr,
	}, checkGame)
	registerType(TypeInfo{
		Type: "steam", Category: "game",
		Fields: []Field{fHost, {Name: "port", Kind: "number", Required: true},
			{Name: "api_key", Kind: "password", Required: true, Secret: true}},
		target: targetAddr,
	}, checkSteam)
}

func checkGame(ctx context.Context, s *Spec, env *Env) Result {
	p := paramsOf(s)
	start := time.Now()
	var info map[string]string
	var err error
	network := "udp"
	if p["game"] == "minecraft" {
		network = "tcp"
	}
	conn, err := env.Dial(ctx, network, p.addr())
	if err != nil {
		return fail(reasonOf(ctx, err), err)
	}
	defer conn.Close()
	switch p["game"] {
	case "source":
		info, err = queryA2S(ctx, conn)
	case "minecraft":
		info, err = queryMinecraft(ctx, conn, p)
	case "minecraft-bedrock":
		info, err = queryBedrock(ctx, conn)
	default:
		return fail("config", fmt.Errorf("unknown game %q", p["game"]))
	}
	if err != nil {
		var bad *badGameReply
		if errors.As(err, &bad) {
			return fail("bad_response", err)
		}
		if network == "udp" {
			return failUDP(ctx, env, err)
		}
		return fail(reasonOf(ctx, err), err)
	}
	return Result{LatencyMS: ms(time.Since(start)), Info: info}
}

type badGameReply struct{ what string }

func (e *badGameReply) Error() string { return "not a game server answer: " + e.what }

var (
	a2sPrefix = []byte{0xff, 0xff, 0xff, 0xff}
	a2sQuery  = append(append([]byte{}, a2sPrefix...), append([]byte{'T'}, []byte("Source Engine Query\x00")...)...)
)

// queryA2S asks for A2S_INFO. Servers since late 2020 first answer with a
// challenge that must be sent back.
func queryA2S(ctx context.Context, conn net.Conn) (map[string]string, error) {
	accept := func(b []byte) bool { return len(b) > 5 && bytes.HasPrefix(b, a2sPrefix) }
	req := a2sQuery
	for range 3 {
		b, err := udpExchange(ctx, conn, req, accept)
		if err != nil {
			return nil, err
		}
		switch b[4] {
		case 'A':
			if len(b) < 9 {
				return nil, &badGameReply{"short challenge"}
			}
			req = append(append([]byte{}, a2sQuery...), b[5:9]...)
		case 'I':
			return parseA2SInfo(b[5:])
		case 'm': // obsolete GoldSource answer: alive, but the fields differ
			return map[string]string{"protocol": "goldsrc"}, nil
		default:
			return nil, &badGameReply{fmt.Sprintf("type %q", b[4])}
		}
	}
	return nil, &badGameReply{"challenge loop"}
}

func parseA2SInfo(b []byte) (map[string]string, error) {
	if len(b) < 1 {
		return nil, &badGameReply{"short info"}
	}
	rest := b[1:] // protocol version
	var parts [4]string
	for i := range parts {
		end := bytes.IndexByte(rest, 0)
		if end < 0 {
			return nil, &badGameReply{"truncated info"}
		}
		parts[i], rest = string(rest[:end]), rest[end+1:]
	}
	info := map[string]string{"name": clip(parts[0], 120), "map": clip(parts[1], 60), "game": clip(parts[3], 60)}
	if len(rest) >= 4 { // app id (2 bytes), players, max players
		info["players"] = strconv.Itoa(int(rest[2]))
		info["max_players"] = strconv.Itoa(int(rest[3]))
	}
	return info, nil
}

func appendVarint(b []byte, v uint32) []byte {
	for {
		c := byte(v & 0x7f)
		v >>= 7
		if v == 0 {
			return append(b, c)
		}
		b = append(b, c|0x80)
	}
}

func readVarint(r io.ByteReader) (int, error) {
	var v int
	for i := range 5 {
		c, err := r.ReadByte()
		if err != nil {
			return 0, err
		}
		v |= int(c&0x7f) << (7 * i)
		if c&0x80 == 0 {
			return v, nil
		}
	}
	return 0, &badGameReply{"varint too long"}
}

// queryMinecraft does the Server List Ping of Java edition.
func queryMinecraft(ctx context.Context, conn net.Conn, p params) (map[string]string, error) {
	stop := closeOnDone(ctx, conn)
	defer stop()
	body := appendVarint([]byte{0}, 0xffffffff) // packet 0, protocol version -1: just asking
	body = appendVarint(body, uint32(len(p["host"])))
	body = append(body, p["host"]...)
	body = binary.BigEndian.AppendUint16(body, uint16(p.int("port")))
	body = appendVarint(body, 1) // next state: status
	frame := append(appendVarint(nil, uint32(len(body))), body...)
	frame = append(frame, 1, 0) // status request
	if _, err := conn.Write(frame); err != nil {
		return nil, err
	}
	br := bufio.NewReader(conn)
	length, err := readVarint(br)
	if err != nil {
		return nil, err
	}
	if length <= 0 || length > 1<<20 {
		return nil, &badGameReply{"bad frame length"}
	}
	data := make([]byte, length)
	if _, err := io.ReadFull(br, data); err != nil {
		return nil, err
	}
	r := bytes.NewReader(data)
	if id, err := readVarint(r); err != nil || id != 0 {
		return nil, &badGameReply{"unexpected packet"}
	}
	n, err := readVarint(r)
	if err != nil || n > r.Len() {
		return nil, &badGameReply{"bad status string"}
	}
	raw := make([]byte, n)
	io.ReadFull(r, raw)
	var st struct {
		Version struct {
			Name string `json:"name"`
		} `json:"version"`
		Players struct {
			Max    int `json:"max"`
			Online int `json:"online"`
		} `json:"players"`
	}
	if err := json.Unmarshal(raw, &st); err != nil {
		return nil, &badGameReply{"status is not JSON"}
	}
	return map[string]string{"version": clip(st.Version.Name, 60),
		"players": strconv.Itoa(st.Players.Online), "max_players": strconv.Itoa(st.Players.Max)}, nil
}

// raknetMagic is the fixed marker of RakNet offline messages.
var raknetMagic = []byte{0x00, 0xff, 0xff, 0x00, 0xfe, 0xfe, 0xfe, 0xfe, 0xfd, 0xfd, 0xfd, 0xfd, 0x12, 0x34, 0x56, 0x78}

// queryBedrock sends an unconnected ping and reads the MOTD string of the pong.
func queryBedrock(ctx context.Context, conn net.Conn) (map[string]string, error) {
	req := []byte{0x01}
	req = binary.BigEndian.AppendUint64(req, uint64(time.Now().UnixMilli()))
	req = append(req, raknetMagic...)
	guid := make([]byte, 8)
	rand.Read(guid)
	req = append(req, guid...)
	b, err := udpExchange(ctx, conn, req, func(b []byte) bool { return len(b) > 35 && b[0] == 0x1c })
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(b[17:33], raknetMagic) {
		return nil, &badGameReply{"no RakNet marker"}
	}
	n := int(binary.BigEndian.Uint16(b[33:35]))
	if 35+n > len(b) {
		return nil, &badGameReply{"truncated pong"}
	}
	motd := strings.Split(string(b[35:35+n]), ";")
	if len(motd) < 6 || !strings.HasPrefix(motd[0], "MC") {
		return nil, &badGameReply{"unexpected MOTD"}
	}
	return map[string]string{"name": clip(motd[1], 120), "version": clip(motd[3], 40), "players": motd[4], "max_players": motd[5]}, nil
}

// steamBase is replaced in tests.
var steamBase = "https://api.steampowered.com"

// checkSteam asks Steam's master list whether it sees the server, as Kuma does.
func checkSteam(ctx context.Context, s *Spec, env *Env) Result {
	p := paramsOf(s)
	ip := p["host"]
	if net.ParseIP(ip) == nil { // the addr filter only takes addresses
		ips, err := net.DefaultResolver.LookupIP(ctx, "ip4", ip)
		if err != nil || len(ips) == 0 {
			if err == nil {
				err = errors.New("no IPv4 address")
			}
			return fail(reasonOf(ctx, err), err)
		}
		ip = ips[0].String()
	}
	q := url.Values{"filter": {`addr\` + ip + ":" + p["port"]}, "key": {p["api_key"]}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, steamBase+"/IGameServersService/GetServerList/v1/?"+q.Encode(), nil)
	if err != nil {
		return fail("config", plainErr(err))
	}
	client := newHTTPClient(env, false)
	defer client.CloseIdleConnections()
	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		err = plainErr(err)
		return fail(reasonOf(ctx, err), err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fail(reasonOf(ctx, err), err)
	}
	switch {
	case resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusUnauthorized:
		return fail("auth", errors.New("Steam refused the API key"))
	case resp.StatusCode != http.StatusOK:
		return fail("http_status", fmt.Errorf("HTTP %d", resp.StatusCode))
	}
	var out struct {
		Response struct {
			Servers []struct {
				Name string `json:"name"`
			} `json:"servers"`
		} `json:"response"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return fail("bad_response", errors.New("Steam answered with something that is not a server list"))
	}
	r := Result{LatencyMS: ms(time.Since(start))}
	if len(out.Response.Servers) == 0 {
		r.Status, r.Reason, r.Error = Down, "not_found", "Steam does not list this server"
		return r
	}
	r.Info = map[string]string{"name": clip(out.Response.Servers[0].Name, 120)}
	return r
}
