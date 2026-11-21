package wire

import (
	"encoding/hex"
	"encoding/json"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
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
		JoinProof struct {
			Redeemer string `json:"redeemer"`
			Value    string `json:"value"`
		} `json:"joinProof"`
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
	if d, _ := Decode64(v.Keys[v.JoinProof.Redeemer], 32); JoinProof(c.Secret, d) != v.JoinProof.Value || !CheckJoinProof(c.Secret, d, v.JoinProof.Value) {
		t.Errorf("join proof: %s, want %s", JoinProof(c.Secret, d), v.JoinProof.Value)
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
	if !reflect.DeepEqual(got, w) {
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
	if got, err := OpenMail(read, priv); err != nil || !reflect.DeepEqual(got, content) {
		t.Fatalf("opened %+v, %v", got, err)
	}
	if _, err := OpenMail(read, other); err == nil {
		t.Fatal("opened with another key")
	}
}

// signed signs a raw payload as a group event, for payloads SignEvent
// wouldn't write.
func signed(k Key, payload string) Envelope {
	return Envelope{Payload: Encode64([]byte(payload)), Signature: Encode64(k.sign(append([]byte(groupEventLabel), payload...)))}
}

// Settings with a member absent take its default (groups section 3.3).
func TestGroupSettingsDefaults(t *testing.T) {
	a := NewKey()
	me := Encode64(a.Public())
	s := NewGroupState()
	ev := signed(a, `{"type":"founded","signer":"`+me+`","member":"`+me+`","seq":1,"date":1800000000,"ownerSeq":1,"name":"F","nonce":"AAAAAAAAAAAAAAAAAAAAAA","settings":{"capPerDay":7}}`)
	if _, _, err := s.Apply(ev, time.Unix(1800000000, 0)); err != nil {
		t.Fatal(err)
	}
	if s.Settings != (GroupSettings{WaitDays: 3, CapPerDay: 7}) {
		t.Errorf("settings %+v, want waitDays's default 3 and capPerDay 7", s.Settings)
	}
}

// A group's name that fits 80 code points but not the length byte is cut
// a whole code point at a time, and the code reads back.
func TestGroupCodeLongName(t *testing.T) {
	k := NewKey()
	c := Code{Address: "https://media.example.org", Key: k.Public(), Secret: make([]byte, 32), Expires: time.Unix(1800000000, 0),
		Group: make([]byte, 32), GroupName: strings.Repeat("😀", 80)}
	back, err := ParseCode(c.String())
	if err != nil {
		t.Fatal(err)
	}
	if back.GroupName != strings.Repeat("😀", 63) || back.Address != c.Address {
		t.Errorf("name %d bytes, address %q", len(back.GroupName), back.Address)
	}
}

// A member's revoked event names one of its own keys only.
func TestGroupRevokedOwnKeyOnly(t *testing.T) {
	a, other := NewKey(), NewKey()
	me := Encode64(a.Public())
	now := time.Unix(1800000000, 0)
	s := NewGroupState()
	founded, _, _ := SignEvent(a, Event{Type: EventFounded, Signer: me, Member: me, Seq: 1, Date: now.Unix(), OwnerSeq: 1, Name: "F", Nonce: "AAAAAAAAAAAAAAAAAAAAAA"})
	if _, _, err := s.Apply(founded, now); err != nil {
		t.Fatal(err)
	}
	g := s.ID
	statement := func(k Key) json.RawMessage {
		r := SignRevocation(k, now)
		return json.RawMessage(`{"key":"` + Encode64(r.Key) + `","date":` + strconv.FormatInt(r.Date, 10) + `,"signature":"` + Encode64(r.Signature) + `"}`)
	}
	last := int64(1)
	foreign, _, _ := SignEvent(a, Event{Group: g, Type: EventRevoked, Signer: me, Member: me, Seq: 2, Date: now.Unix(), LastSeq: &last, Statement: statement(other)})
	if _, _, err := s.Apply(foreign, now); err == nil {
		t.Error("another key's revocation was kept")
	}
	own, _, _ := SignEvent(a, Event{Group: g, Type: EventRevoked, Signer: me, Member: me, Seq: 2, Date: now.Unix(), LastSeq: &last, Statement: statement(a)})
	if _, _, err := s.Apply(own, now); err != nil {
		t.Errorf("its own key's revocation: %v", err)
	}
}
