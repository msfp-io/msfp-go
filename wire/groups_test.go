package wire

import (
	"encoding/hex"
	"slices"
	"sort"
	"testing"
	"time"
)

// The groups part against msfp-spec's vectors/v0/groups.json: each
// event's verdict, the state they give, the code with a group, the mail.
func TestGroupVectors(t *testing.T) {
	var v struct {
		Keys   map[string]string `json:"keys"`
		Now    int64             `json:"now"`
		Events []struct {
			Name      string `json:"name"`
			Event     string `json:"event"`
			Signature string `json:"signature"`
			ID        string `json:"id"`
			Valid     bool   `json:"valid"`
		} `json:"events"`
		State struct {
			ID       string        `json:"id"`
			Name     string        `json:"name"`
			Settings GroupSettings `json:"settings"`
			Owner    string        `json:"owner"`
			Admins   []string      `json:"admins"`
			Members  []string      `json:"members"`
			Banned   []string      `json:"banned"`
			Heir     string        `json:"heir"`
			Excluded []string      `json:"excluded"`
		} `json:"state"`
		Code struct {
			Text    string `json:"text"`
			Address string `json:"address"`
		} `json:"code"`
		Group, GroupName string
		Mail             struct {
			Mail      string `json:"mail"`
			Signature string `json:"signature"`
			Private   string `json:"recipientX25519Private"`
			Opened    string `json:"opened"`
		} `json:"mail"`
	}
	readVectors(t, "groups.json", &v)
	s := NewGroupState()
	now := time.Unix(v.Now, 0)
	for _, e := range v.Events {
		_, id, err := s.Apply(Envelope{Payload: e.Event, Signature: e.Signature}, now)
		if (err == nil) != e.Valid {
			t.Errorf("%s: kept %v, the vector says %v (%v)", e.Name, err == nil, e.Valid, err)
		}
		if id != "" && id != e.ID {
			t.Errorf("%s: id %s, want %s", e.Name, id, e.ID)
		}
	}
	var admins, members []string
	for id, m := range s.Members {
		members = append(members, id)
		if m.Admin {
			admins = append(admins, id)
		}
	}
	banned := make([]string, 0)
	for id := range s.Banned {
		banned = append(banned, id)
	}
	for _, l := range [][]string{admins, members, banned, s.Excluded, v.State.Admins, v.State.Members, v.State.Banned, v.State.Excluded} {
		sort.Strings(l)
	}
	if s.ID != v.State.ID || s.Name != v.State.Name || s.Owner != v.State.Owner || s.Heir != v.State.Heir ||
		s.Settings != v.State.Settings || !slices.Equal(admins, v.State.Admins) || !slices.Equal(members, v.State.Members) ||
		!slices.Equal(banned, v.State.Banned) || !slices.Equal(s.Excluded, v.State.Excluded) {
		t.Errorf("state: %+v\nwant %+v", s, v.State)
	}
	c, err := ParseCode(v.Code.Text)
	if err != nil || Encode64(c.Group) != v.Group || c.GroupName != v.GroupName || c.Address != v.Code.Address {
		t.Errorf("code: %+v, %v", c, err)
	}
	n, _ := Decode64(v.Keys["n"], 32)
	m, err := ReadMail(Envelope{Mail: v.Mail.Mail, Signature: v.Mail.Signature}, n)
	if err != nil {
		t.Fatal(err)
	}
	priv, _ := hex.DecodeString(v.Mail.Private)
	got, err := OpenMail(m, priv)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := Marshal(got)
	var w Sealed
	_ = DecodeJSON([]byte(v.Mail.Opened), &w)
	if got != w {
		t.Errorf("opened %s, want %s", want, v.Mail.Opened)
	}
}

// A mail sealed here opens with the recipient's key, and only with it.
func TestMailRoundTrip(t *testing.T) {
	from := NewKey()
	priv, pub, _ := MailKeyPair(make([]byte, 32))
	other, _, _ := MailKeyPair(append(make([]byte, 31), 1))
	m := Mail{Group: Encode64(make([]byte, 32)), ID: Encode64(make([]byte, 16)), Kind: "request",
		From: Encode64(from.Public()), To: Encode64(make([]byte, 32)), Expires: 1800000000}
	content := Sealed{Address: "https://n.example.org", NextCommitment: Encode64(make([]byte, 32)), Inbound: true}
	env, err := SealMail(from, m, pub, content)
	if err != nil {
		t.Fatal(err)
	}
	read, err := ReadMail(env, from.Public())
	if err != nil {
		t.Fatal(err)
	}
	if got, err := OpenMail(read, priv); err != nil || got != content {
		t.Fatalf("opened %+v, %v", got, err)
	}
	if _, err := OpenMail(read, other); err == nil {
		t.Fatal("opened with another key")
	}
}
