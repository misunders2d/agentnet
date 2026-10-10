package hub

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/misunders2d/agentnet/internal/googleauth"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

// Appended schema step: existing enrollments and roster chains are untouched.
const googleSchema = `
CREATE TABLE google_emails(email TEXT PRIMARY KEY, admin INTEGER NOT NULL DEFAULT 0, denied INTEGER NOT NULL DEFAULT 0);
CREATE TABLE google_domains(domain TEXT PRIMARY KEY);
CREATE TABLE google_people(email TEXT PRIMARY KEY, subject TEXT NOT NULL, person TEXT NOT NULL UNIQUE);
`

var errGoogleAccess = errors.New("this email is not invited to this workspace; ask its admin")

func googleAllowed(q querier, c googleauth.Claims) (bool, error) {
	_, domain, _ := strings.Cut(c.Email, "@")
	if c.Domain != domain && domain != "gmail.com" && domain != "googlemail.com" {
		return false, errGoogleAccess
	}
	var admin, denied bool
	err := q.QueryRow(`SELECT admin, denied FROM google_emails WHERE email = ?`, c.Email).Scan(&admin, &denied)
	if err == nil {
		if denied {
			return false, errGoogleAccess
		}
		return admin, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	// Google is authoritative for Workspace domains only when hd agrees.
	// A third-party Google account cannot claim the company's domain.
	if c.Domain != domain {
		return false, errGoogleAccess
	}
	var n int
	if err := q.QueryRow(`SELECT count(*) FROM google_domains WHERE domain = ?`, domain).Scan(&n); err != nil {
		return false, err
	}
	if n == 0 {
		return false, errGoogleAccess
	}
	return false, nil
}

func (h *Hub) handleGoogleConfig(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, protocol.GoogleConfig{WebClientID: h.cfg.GoogleWebClientID, DesktopClientID: h.cfg.GoogleDesktopClientID})
}

func (h *Hub) readGoogle(w http.ResponseWriter, r *http.Request) (protocol.GoogleRequest, googleauth.Claims, bool) {
	var req protocol.GoogleRequest
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, protocol.MaxBody))
	if err != nil || decodeStrict(b, &req) != nil || req.Verify() != nil {
		writeError(w, http.StatusBadRequest, "", "malformed signed Google request")
		return req, googleauth.Claims{}, false
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	c, err := h.google.Verify(ctx, req.IDToken, protocol.GoogleNonce(req.Public))
	if err != nil {
		writeError(w, http.StatusUnauthorized, "", googleauth.ErrCredential.Error())
		return req, c, false
	}
	return req, c, true
}

func (h *Hub) handleGooglePrepare(w http.ResponseWriter, r *http.Request) {
	req, c, ok := h.readGoogle(w, r)
	if !ok {
		return
	}
	if _, err := googleAllowed(h.store.db, c); err != nil {
		googleError(w, err)
		return
	}
	_, name, err := protocol.SplitAddress(req.Public.Address)
	if err != nil {
		writeError(w, 400, "", "invalid device name")
		return
	}
	label := protocol.GoogleLabel(c.Email)
	p := protocol.GooglePrepared{Email: c.Email, Name: c.Name, Address: protocol.Address(label, name)}
	var person, subject string
	err = h.store.db.QueryRow(`SELECT person, subject FROM google_people WHERE email = ?`, c.Email).Scan(&person, &subject)
	if err == nil {
		if subject != c.Subject {
			googleError(w, errGoogleAccess)
			return
		}
		head, found, err := personHeadIn(h.store.db, person)
		if err != nil || !found {
			googleError(w, errors.New("missing person roster"))
			return
		}
		r, err := protocol.ParsePersonRoster(head.record)
		if err != nil {
			googleError(w, err)
			return
		}
		p.Head = &r
		for _, d := range r.Devices {
			a, err := h.store.agent(d.Address)
			if err == nil && !a.Revoked && !a.Pending && a.Person == person {
				copy := d
				p.Approver = &copy
				break
			}
		}
		if p.Approver == nil {
			writeError(w, 409, "", "no existing device can approve; contact your workspace admin")
			return
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		googleError(w, err)
		return
	}
	// Exact-key retries keep their original address, including a pending
	// device. Neither a revoked key nor a different person's key is reused.
	rows, err := h.store.db.Query(`SELECT address, public FROM agents WHERE label = ? AND revoked_at IS NULL`, label)
	if err != nil {
		googleError(w, err)
		return
	}
	for rows.Next() {
		var address, raw string
		if err := rows.Scan(&address, &raw); err != nil {
			rows.Close()
			googleError(w, err)
			return
		}
		var dev identity.Public
		json.Unmarshal([]byte(raw), &dev)
		if dev.Fingerprint() == req.Public.Fingerprint() && dev.BoxRecipient == req.Public.BoxRecipient {
			p.Address = address
			p.Enrolled = true
			rows.Close()
			writeJSON(w, 200, p)
			return
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		googleError(w, err)
		return
	}
	rows.Close()
	// Revoked addresses stay taken too.
	var exists int
	if err := h.store.db.QueryRow(`SELECT count(*) FROM agents WHERE address = ?`, p.Address).Scan(&exists); err != nil {
		googleError(w, err)
		return
	}
	if exists != 0 {
		tx, err := h.store.db.Begin()
		if err != nil {
			googleError(w, err)
			return
		}
		p.Address, err = freeAddress(tx, label, p.Address)
		tx.Rollback()
		if err != nil {
			googleError(w, err)
			return
		}
	}
	writeJSON(w, 200, p)
}

func googleError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errGoogleAccess):
		writeError(w, 403, "", errGoogleAccess.Error())
	case errors.Is(err, errRosterStale):
		writeError(w, 409, protocol.CodeRosterStale, errRosterStale.Error())
	case errors.Is(err, errTooManyDevices):
		writeError(w, 409, protocol.CodeTooManyDevices, errTooManyDevices.Error())
	case errors.Is(err, errAddressTaken):
		writeError(w, 409, protocol.CodeAddressTaken, "device name is taken; sign in again")
	case errors.Is(err, errBadStep):
		writeError(w, 400, protocol.CodeBadStep, "invalid Google person or device consent")
	default:
		writeError(w, 500, "", "storage error")
	}
}

func (h *Hub) handleGoogleJoin(w http.ResponseWriter, r *http.Request) {
	req, c, ok := h.readGoogle(w, r)
	if !ok {
		return
	}
	inviter, changed, err := h.store.enrollGoogle(c, req)
	if err != nil {
		googleError(w, err)
		return
	}
	if changed {
		if inviter != "" {
			h.linksGen.Add(1)
			h.streams.notifyAll() // each active device replays only its own person's pending requests
		} else {
			h.membersChanged()
		}
	}
	writeJSON(w, 201, protocol.DirectoryEntry{Public: req.Public})
}

func (s *store) enrollGoogle(c googleauth.Claims, req protocol.GoogleRequest) (string, bool, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return "", false, err
	}
	defer tx.Rollback()
	admin, err := googleAllowed(tx, c)
	if err != nil {
		return "", false, err
	}
	label, _, err := protocol.SplitAddress(req.Public.Address)
	if err != nil || label != protocol.GoogleLabel(c.Email) || (req.First == nil) == (req.Link == nil) {
		return "", false, errBadStep
	}
	data, _ := json.Marshal(req.Public)
	if a, err := agentIn(tx, req.Public.Address); err == nil {
		var email, subject string
		person := a.Person
		if a.Pending {
			err = tx.QueryRow(`SELECT pending_person FROM agents WHERE address = ?`, a.Public.Address).Scan(&person)
			if err != nil {
				return "", false, err
			}
		}
		err = tx.QueryRow(`SELECT email, subject FROM google_people WHERE person = ?`, person).Scan(&email, &subject)
		if err == nil && email == c.Email && subject == c.Subject && !a.Revoked && a.Public.Fingerprint() == req.Public.Fingerprint() && a.Public.BoxRecipient == req.Public.BoxRecipient {
			return "", false, nil
		}
		return "", false, errAddressTaken
	} else if !errors.Is(err, errNotFound) {
		return "", false, err
	}
	var person, subject string
	err = tx.QueryRow(`SELECT person, subject FROM google_people WHERE email = ?`, c.Email).Scan(&person, &subject)
	now := time.Now().Unix()
	if errors.Is(err, sql.ErrNoRows) {
		r := req.First
		if r == nil || r.Email != c.Email || r.VerifyFirst() != nil || !r.Has(req.Public.Address, req.Public.Fingerprint()) || r.Devices[0].BoxRecipient != req.Public.BoxRecipient {
			return "", false, errBadStep
		}
		if _, found, err := personHeadIn(tx, r.Person); err != nil {
			return "", false, err
		} else if found {
			return "", false, errRosterStale
		}
		raw, _ := json.Marshal(r)
		if len(raw) > protocol.MaxPersonRecord {
			return "", false, errBadStep
		}
		if _, err := tx.Exec(`INSERT INTO agents(address,label,public,admin,created_at,person_id,linked) VALUES(?,?,?,?,?,?,1)`, req.Public.Address, label, string(data), admin, now, r.Person); err != nil {
			return "", false, err
		}
		if _, err := tx.Exec(`INSERT INTO persons(person,seq,hash,record) VALUES(?,0,?,?)`, r.Person, r.Hash(), string(raw)); err != nil {
			return "", false, err
		}
		if _, err := tx.Exec(`INSERT INTO person_chain(person,seq,hash,record) VALUES(?,0,?,?)`, r.Person, r.Hash(), string(raw)); err != nil {
			return "", false, err
		}
		if _, err := tx.Exec(`INSERT INTO google_people(email,subject,person) VALUES(?,?,?)`, c.Email, c.Subject, r.Person); err != nil {
			return "", false, err
		}
		return "", true, tx.Commit()
	}
	if err != nil {
		return "", false, err
	}
	if subject != c.Subject {
		return "", false, errGoogleAccess
	}
	l := req.Link
	if l == nil {
		return "", false, errRosterStale
	}
	head, found, err := personHeadIn(tx, person)
	if err != nil {
		return "", false, err
	}
	if !found || l.Email != c.Email || l.Person != person || l.Seq != head.seq || l.Roster != head.hash {
		return "", false, errRosterStale
	}
	r, err := protocol.ParsePersonRoster(head.record)
	if err != nil {
		return "", false, err
	}
	approver, has := r.Device(l.Approver.Fingerprint)
	if !has || approver.Address != l.Approver.Address || !protocol.ValidID(l.Offer) || l.Expires <= now || l.Expires > now+protocol.MaxLinkTTL ||
		!ed25519.Verify(req.Public.SignKey, protocol.JoinBytes(person, head.seq+1, head.hash, req.Public), l.Join) {
		return "", false, errBadStep
	}
	a, err := agentIn(tx, approver.Address)
	if err != nil || a.Revoked || a.Pending || a.Person != person {
		return "", false, errBadStep
	}
	if room, err := devicesAvailable(tx, person, head); err != nil {
		return "", false, err
	} else if !room {
		return "", false, errTooManyDevices
	}
	ev, _ := json.Marshal(protocol.LinkEvent{Offer: l.Offer, Device: req.Public, Join: l.Join, Google: l})
	if _, err := tx.Exec(`INSERT INTO agents(address,label,public,admin,created_at,linked,pending_person,pending_inviter,pending_until,pending_event) VALUES(?,?,?,0,?,1,?,?,?,?)`,
		req.Public.Address, label, string(data), now, person, approver.Address, l.Expires+protocol.PendingGrace, string(ev)); err != nil {
		return "", false, err
	}
	return approver.Address, true, tx.Commit()
}

func (h *Hub) handleGoogleAccess(w http.ResponseWriter, r *http.Request) {
	caller, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	a, err := h.store.agent(caller)
	if err != nil {
		googleError(w, err)
		return
	}
	v := protocol.GoogleAccess{WorkspaceURL: h.cfg.PublicURL, Enabled: h.cfg.GoogleWebClientID != "" || h.cfg.GoogleDesktopClientID != "", CanAdmin: a.Admin, Emails: []protocol.GoogleEmail{}, Domains: []string{}}
	if a.Admin {
		rows, err := h.store.db.Query(`SELECT email,admin,denied,domain_member FROM (
 SELECT email,admin,denied,0 AS domain_member FROM google_emails
 UNION ALL SELECT g.email,0,0,1 FROM google_people g WHERE NOT EXISTS (SELECT 1 FROM google_emails e WHERE e.email=g.email)
) ORDER BY email`)
		if err != nil {
			googleError(w, err)
			return
		}
		for rows.Next() {
			var e protocol.GoogleEmail
			if err := rows.Scan(&e.Email, &e.Admin, &e.Denied, &e.DomainMember); err != nil {
				rows.Close()
				googleError(w, err)
				return
			}
			v.Emails = append(v.Emails, e)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			googleError(w, err)
			return
		}
		rows, err = h.store.db.Query(`SELECT domain FROM google_domains ORDER BY domain`)
		if err != nil {
			googleError(w, err)
			return
		}
		for rows.Next() {
			var d string
			if err := rows.Scan(&d); err != nil {
				rows.Close()
				googleError(w, err)
				return
			}
			v.Domains = append(v.Domains, d)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			googleError(w, err)
			return
		}
	}
	writeJSON(w, 200, v)
}

func (h *Hub) handleGoogleAccessChange(w http.ResponseWriter, r *http.Request) {
	_, body, ok := h.requireAdmin(w, r)
	if !ok {
		return
	}
	var c protocol.GoogleAccessChange
	if decodeStrict(body, &c) != nil || (c.Email == "") == (c.Domain == "") || c.Domain != "" && c.Admin != nil {
		writeError(w, 400, "", "choose one email or domain")
		return
	}
	var err error
	if c.Email != "" {
		c.Email, err = protocol.NormalizeEmail(c.Email)
	} else {
		c.Domain, err = protocol.NormalizeEmailDomain(c.Domain)
	}
	if err != nil {
		writeError(w, 400, "", err.Error())
		return
	}
	addresses, err := h.store.changeGoogleAccess(c)
	if err != nil {
		googleError(w, err)
		return
	}
	for _, address := range addresses {
		h.streams.disconnect(address)
	}
	h.lookAgain() // an admin role may have changed, which the list does not show
	w.WriteHeader(204)
}

func (s *store) changeGoogleAccess(c protocol.GoogleAccessChange) ([]string, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var addresses []string
	if c.Domain != "" {
		if c.Remove {
			_, err = tx.Exec(`DELETE FROM google_domains WHERE domain = ?`, c.Domain)
		} else {
			_, err = tx.Exec(`INSERT OR IGNORE INTO google_domains(domain) VALUES(?)`, c.Domain)
		}
	} else {
		_, err = tx.Exec(`INSERT INTO google_emails(email,admin,denied) VALUES(?,?,?) ON CONFLICT(email) DO UPDATE SET admin=CASE WHEN excluded.denied THEN 0 WHEN ? THEN 1 ELSE google_emails.admin END,denied=excluded.denied`, c.Email, c.Admin != nil && *c.Admin && !c.Remove, c.Remove, c.Admin)
		if err == nil && c.Remove {
			rows, e := tx.Query(`SELECT address FROM agents WHERE revoked_at IS NULL AND (person_id IN (SELECT person FROM google_people WHERE email=?) OR pending_person IN (SELECT person FROM google_people WHERE email=?) OR label=?)`, c.Email, c.Email, protocol.GoogleLabel(c.Email))
			if e != nil {
				return nil, e
			}
			for rows.Next() {
				var a string
				if e := rows.Scan(&a); e != nil {
					rows.Close()
					return nil, e
				}
				addresses = append(addresses, a)
			}
			e = rows.Err()
			rows.Close()
			if e != nil {
				return nil, e
			}
			for _, a := range addresses {
				if _, err = tx.Exec(`UPDATE agents SET revoked_at=?,revoked_reason='removed' WHERE address=?`, time.Now().Unix(), a); err != nil {
					return nil, err
				}
				if err = dropNotify(tx, a); err != nil {
					return nil, err
				}
			}
			_, err = tx.Exec(`DELETE FROM google_people WHERE email=?`, c.Email)
		} else if err == nil && c.Admin != nil && *c.Admin {
			_, err = tx.Exec(`UPDATE agents SET admin=? WHERE person_id IN (SELECT person FROM google_people WHERE email=?) AND revoked_at IS NULL`, c.Admin, c.Email)
		}
	}
	if err != nil {
		return nil, err
	}
	return addresses, tx.Commit()
}

func (h *Hub) handleGoogleExchange(w http.ResponseWriter, r *http.Request) {
	var e protocol.GoogleExchange
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, protocol.MaxBody))
	if err != nil || decodeStrict(body, &e) != nil || e.Verify() != nil || len(e.Code) == 0 || len(e.Code) > 4096 || len(e.Verifier) < 43 || len(e.Verifier) > 128 {
		writeError(w, 400, "", "invalid Google exchange")
		return
	}
	u, err := url.Parse(e.Redirect)
	port, portErr := strconv.Atoi(uPort(u))
	if err != nil || u == nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || portErr != nil || port < 1 || port > 65535 || u.Path != "/oauth/google" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		writeError(w, 400, "", "invalid loopback redirect")
		return
	}
	if h.cfg.GoogleDesktopClientID == "" {
		writeError(w, 503, "", "workspace admin must configure Google Desktop sign-in")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	httpClient := h.cfg.GoogleHTTP
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 15 * time.Second}
	}
	safe := *httpClient
	safe.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	ctx = context.WithValue(ctx, oauth2.HTTPClient, &safe)
	cfg := oauth2.Config{ClientID: h.cfg.GoogleDesktopClientID, ClientSecret: h.cfg.GoogleDesktopClientSecret, RedirectURL: e.Redirect, Endpoint: google.Endpoint}
	token, err := cfg.Exchange(ctx, e.Code, oauth2.VerifierOption(e.Verifier))
	if err != nil {
		writeError(w, 401, "", googleauth.ErrCredential.Error())
		return
	}
	id, _ := token.Extra("id_token").(string)
	if id == "" {
		writeError(w, 401, "", googleauth.ErrCredential.Error())
		return
	}
	if _, err := h.google.Verify(ctx, id, protocol.GoogleNonce(e.Public)); err != nil {
		writeError(w, 401, "", googleauth.ErrCredential.Error())
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, struct {
		IDToken string `json:"id_token"`
	}{id})
}

func uPort(u *url.URL) string {
	if u == nil {
		return ""
	}
	return u.Port()
}
