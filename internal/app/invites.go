package app

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/wh3r3ar3you/uptimeforge/internal/hub"
)

const kindInvite = "invite"

// InviteTTL is how long an invite code can be exchanged.
const InviteTTL = 24 * time.Hour

var (
	ErrInviteUnknown = errors.New("unknown invite code")
	ErrInviteUsed    = errors.New("this invite code has already been used")
	ErrInviteExpired = errors.New("this invite code has expired")
)

// Invite is a one-time code an admin hands to a volunteer: exchanged once,
// it becomes a checker probe and that probe's token. Only the code's hash
// is kept.
type Invite struct {
	ID      string    `json:"id"`
	Name    string    `json:"name,omitempty"`
	Trusted bool      `json:"trusted"`
	Created time.Time `json:"created"`
	Expires time.Time `json:"expires"`
	Used    bool      `json:"used"`
}

// storedInvite is an invite as it is in the database.
type storedInvite struct {
	Invite
	CodeHash string `json:"code_hash"`
}

func hashCode(code string) string {
	sum := sha256.Sum256([]byte(code))
	return hex.EncodeToString(sum[:])
}

// CreateInvite issues a code good for one checker within InviteTTL. The code
// is returned only here.
func (a *App) CreateInvite(ctx context.Context, name string, trusted bool) (Invite, string, error) {
	b := make([]byte, 16)
	rand.Read(b)
	code := base64.RawURLEncoding.EncodeToString(b)
	id := make([]byte, 6)
	rand.Read(id)
	now := time.Now().UTC()
	inv := Invite{ID: hex.EncodeToString(id), Name: CleanName(name), Trusted: trusted, Created: now, Expires: now.Add(InviteTTL)}
	a.inviteMu.Lock()
	defer a.inviteMu.Unlock()
	if err := a.Store.PutObject(ctx, kindInvite, inv.ID, storedInvite{Invite: inv, CodeHash: hashCode(code)}); err != nil {
		return Invite{}, "", err
	}
	return inv, code, nil
}

// Invites lists every invite, used and expired ones included.
func (a *App) Invites(ctx context.Context) ([]Invite, error) {
	objs, err := a.Store.Objects(ctx, kindInvite)
	if err != nil {
		return nil, err
	}
	out := make([]Invite, 0, len(objs))
	for _, o := range objs {
		var s storedInvite
		if err := json.Unmarshal(o.Data, &s); err != nil {
			return nil, fmt.Errorf("invite %s: %w", o.ID, err)
		}
		out = append(out, s.Invite)
	}
	return out, nil
}

// RevokeInvite deletes an invite: its code becomes unknown.
func (a *App) RevokeInvite(ctx context.Context, id string) error {
	a.inviteMu.Lock()
	defer a.inviteMu.Unlock()
	return a.Store.DeleteObject(ctx, kindInvite, id)
}

// Join exchanges an invite code for a new checker probe and its agent
// token. The checker is named after the invite, or after the host when the
// invite has no name. A code works once: it is spent before the probe is
// made, and given back only if the probe could not be made.
//
// A started join runs to the end even if the agent hangs up: stopping
// between the probe and the reply would leave a probe whose token nobody
// has, and hand back a code that makes another one.
func (a *App) Join(ctx context.Context, code, hostname, os, arch string) (probeID, token string, err error) {
	ctx = context.WithoutCancel(ctx)
	a.inviteMu.Lock()
	defer a.inviteMu.Unlock()
	inv, err := a.findInvite(ctx, code)
	if err != nil {
		return "", "", err
	}
	switch {
	case inv.Used:
		return "", "", ErrInviteUsed
	case !time.Now().Before(inv.Expires):
		return "", "", ErrInviteExpired
	}
	inv.Used = true
	if err := a.Store.PutObject(ctx, kindInvite, inv.ID, inv); err != nil {
		return "", "", err
	}
	defer func() {
		if err != nil {
			inv.Used = false
			if perr := a.Store.PutObject(ctx, kindInvite, inv.ID, inv); perr != nil {
				a.Log.Error("invite could not be given back after a failed join", "invite", inv.ID, "err", perr)
			}
		}
	}()
	name := inv.Name
	if name == "" {
		name = CleanName(hostname)
	}
	base := slugify(name)
	if base == "" {
		base = "checker"
	}
	for n := 1; n <= 100; n++ {
		id := base
		if n > 1 {
			id = fmt.Sprintf("%s-%d", base, n)
		}
		token, err = a.SaveProbe(ctx, hub.ProbeConfig{ID: id, Name: name, Role: hub.RoleChecker, Trusted: inv.Trusted}, true)
		switch {
		case err == nil:
			return id, token, nil
		case token != "":
			// The probe is stored, only the hub did not take the new config:
			// the next reload brings it in. The exchange itself is done.
			a.Log.Error("checker joined but the hub did not reload", "probe", id, "err", err)
			return id, token, nil
		case errors.Is(err, ErrExists), errors.Is(err, ErrManaged):
			continue
		default:
			return "", "", err
		}
	}
	return "", "", fmt.Errorf("no free probe id for %q", base)
}

// findInvite is the invite whose code hashes to code's hash.
func (a *App) findInvite(ctx context.Context, code string) (storedInvite, error) {
	if code == "" {
		return storedInvite{}, ErrInviteUnknown
	}
	h := []byte(hashCode(code))
	objs, err := a.Store.Objects(ctx, kindInvite)
	if err != nil {
		return storedInvite{}, err
	}
	for _, o := range objs {
		var s storedInvite
		if json.Unmarshal(o.Data, &s) == nil && subtle.ConstantTimeCompare(h, []byte(s.CodeHash)) == 1 {
			return s, nil
		}
	}
	return storedInvite{}, ErrInviteUnknown
}

// CleanName makes a name from someone else's input fit to show: no control
// characters, no surrounding space, at most 64 characters.
func CleanName(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, strings.TrimSpace(s))
	if r := []rune(s); len(r) > 64 {
		s = strings.TrimSpace(string(r[:64]))
	}
	return s
}

// translit spells Cyrillic in Latin letters for probe ids, as the probe form
// does.
var translit = map[rune]string{'а': "a", 'б': "b", 'в': "v", 'г': "g", 'д': "d", 'е': "e", 'ё': "e", 'ж': "zh", 'з': "z",
	'и': "i", 'й': "y", 'к': "k", 'л': "l", 'м': "m", 'н': "n", 'о': "o", 'п': "p", 'р': "r", 'с': "s", 'т': "t", 'у': "u",
	'ф': "f", 'х': "h", 'ц': "c", 'ч': "ch", 'ш': "sh", 'щ': "sch", 'ы': "y", 'э': "e", 'ю': "yu", 'я': "ya"}

// slugify turns a name into a probe id the way the probe form does, short
// enough to take a "-N" suffix.
func slugify(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		if t, ok := translit[r]; ok {
			b.WriteString(t)
			dash = false
			continue
		}
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '-' {
			b.WriteRune(r)
			dash = r == '-'
			continue
		}
		if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	out := b.String()
	if len(out) > 50 {
		out = out[:50]
	}
	return strings.Trim(out, "-_")
}
