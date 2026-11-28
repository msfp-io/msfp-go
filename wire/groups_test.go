package wire

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/rand/v2"
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
	readVectors(t, "msfp/v0/groups.json", &v)
	now := time.Unix(v.Now, 0)
	var envs []Envelope
	for _, e := range v.Events {
		env := Envelope{Payload: e.Event, Signature: e.Signature}
		if _, id, err := CheckEvent(env, v.Group, now); err == nil {
			envs = append(envs, env)
		} else if id != "" && id != e.ID {
			t.Errorf("%s: id %s, want %s", e.Name, id, e.ID)
		}
	}
	s, results := ComputeGroup(v.Group, envs)
	for _, e := range v.Events {
		r, ok := results[e.ID]
		if counted := ok && r.Verdict == Counted; counted != e.Valid {
			t.Errorf("%s: counts %v, the vector says %v (%s)", e.Name, counted, e.Valid, results[e.ID].Reason)
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
	ev := signed(a, `{"type":"founded","signer":"`+me+`","member":"`+me+`","seq":1,"date":1800000000,"ownerSeq":1,"name":"F","nonce":"AAAAAAAAAAAAAAAAAAAAAA","settings":{"capPerDay":7}}`)
	b, _ := Decode64(ev.Payload, -1)
	sum := sha256.Sum256(b)
	s, results := ComputeGroup(Encode64(sum[:]), []Envelope{ev})
	if results[Encode64(sum[:])].Verdict != Counted {
		t.Fatal(results)
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
	founded, g, _ := SignEvent(a, Event{Type: EventFounded, Signer: me, Member: me, Seq: 1, Date: now.Unix(), OwnerSeq: 1, Name: "F", Nonce: "AAAAAAAAAAAAAAAAAAAAAA"})
	statement := func(k Key) json.RawMessage {
		r := SignRevocation(k, now)
		return json.RawMessage(`{"key":"` + Encode64(r.Key) + `","date":` + strconv.FormatInt(r.Date, 10) + `,"signature":"` + Encode64(r.Signature) + `"}`)
	}
	last := int64(1)
	foreign, _, _ := SignEvent(a, Event{Group: g, Type: EventRevoked, Signer: me, Member: me, Seq: 2, Date: now.Unix(), LastSeq: &last, Statement: statement(other)})
	_, results := ComputeGroup(g, []Envelope{founded, foreign})
	if _, id, _ := CheckEvent(foreign, g, now); results[id].Verdict == Counted {
		t.Error("another key's revocation counts")
	}
	own, _, _ := SignEvent(a, Event{Group: g, Type: EventRevoked, Signer: me, Member: me, Seq: 2, Date: now.Unix(), LastSeq: &last, Statement: statement(a)})
	_, results = ComputeGroup(g, []Envelope{founded, own})
	if _, id, _ := CheckEvent(own, g, now); results[id].Verdict != Counted {
		t.Errorf("its own key's revocation: %+v", results[id])
	}
}

// The vectors' events, in any order: the same state and the same verdicts
// (groups section 3.5).
func TestGroupOrderIndependent(t *testing.T) {
	var v struct {
		Group  string `json:"group"`
		Events []struct {
			Event, Signature, ID string
		} `json:"events"`
	}
	readVectors(t, "msfp/v0/groups.json", &v)
	var envs []Envelope
	for _, e := range v.Events {
		envs = append(envs, Envelope{Payload: e.Event, Signature: e.Signature})
	}
	summary := func(s *GroupState, r map[string]EventResult) string {
		var ms []string
		for id, m := range s.Members {
			ms = append(ms, fmt.Sprintf("%s:%v:%s", id, m.Admin, m.Key))
		}
		sort.Strings(ms)
		var vs []string
		for id, x := range r {
			vs = append(vs, fmt.Sprintf("%s:%d", id, x.Verdict))
		}
		sort.Strings(vs)
		ex := slices.Clone(s.Excluded)
		sort.Strings(ex)
		return fmt.Sprint(s.Owner, s.Heir, s.OwnerSeq, s.Settings, ms, ex, len(s.Banned), vs)
	}
	want := summary(ComputeGroup(v.Group, envs))
	rng := rand.New(rand.NewPCG(1, 2))
	for range 50 {
		shuffled := slices.Clone(envs)
		rng.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
		if got := summary(ComputeGroup(v.Group, shuffled)); got != want {
			t.Fatalf("another order, another state:\n%s\nwant\n%s", got, want)
		}
	}
}

// An owner event replayed without counting takes its ownerSeq all the
// same (groups section 3.5): the owner names B admin while B leaves, B's
// left replayed first, so the admin event can't count; the owner's next
// event is still ready, and counts. Before, it waited forever.
func TestGroupOwnerEventNotCountingTakesOwnerSeq(t *testing.T) {
	a, b := NewKey(), NewKey()
	am, bm := Encode64(a.Public()), Encode64(b.Public())
	now := time.Unix(1800000000, 0).Unix()
	founded, g, _ := SignEvent(a, Event{Type: EventFounded, Signer: am, Member: am, Seq: 1, Date: now, OwnerSeq: 1, Name: "F", Nonce: "AAAAAAAAAAAAAAAAAAAAAA"})
	joined, _, _ := SignEvent(a, Event{Group: g, Type: EventJoined, Signer: am, Member: am, Seq: 2, Date: now, OwnerSeq: 2, NewMember: bm, Name: "B"})
	left, leftID, _ := SignEvent(b, Event{Group: g, Type: EventLeft, Signer: bm, Member: bm, Seq: 1, Date: now})
	on := true
	var admin Envelope
	var adminID string
	for d := now; ; d++ {
		admin, adminID, _ = SignEvent(a, Event{Group: g, Type: EventAdmin, Signer: am, Member: am, Seq: 3, Date: d, OwnerSeq: 3, Target: bm, On: &on})
		if leftID < adminID {
			break // B's left is replayed first
		}
	}
	settings, settingsID, _ := SignEvent(a, Event{Group: g, Type: EventSettings, Signer: am, Member: am, Seq: 4, Date: now, OwnerSeq: 4, Name: "G", Settings: &GroupSettings{WaitDays: 1, CapPerDay: 5}})
	s, results := ComputeGroup(g, []Envelope{founded, joined, left, admin, settings})
	if results[adminID].Verdict == Counted {
		t.Fatalf("the admin event counts: %+v", results[adminID])
	}
	if r := results[settingsID]; r.Verdict != Counted {
		t.Fatalf("the owner's next event: %+v", r)
	}
	if s.OwnerSeq != 4 || s.Name != "G" {
		t.Fatalf("state: ownerSeq %d, name %q", s.OwnerSeq, s.Name)
	}
}
