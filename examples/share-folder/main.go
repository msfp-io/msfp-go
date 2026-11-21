// Command share-folder is a small MSFP host: one server, its records in
// JSON files, sharing a folder of films (folder.go) with the servers it
// pairs with by hand, and playing theirs.
//
// Two of them on one machine:
//
//	go run . -address https://127.0.0.1:8441 -listen :8441 -data a -films ~/films -loopback
//	go run . -address https://127.0.0.1:8442 -listen :8442 -data b -loopback
//
// Then type "code" in the first, "redeem <code>" in the second, and
// "accept <id>" in the first. "peers" lists them, "end <id>" ends one.
// "offer <id>" shares the films with a peer; on the other side, "want
// <id>" reads them, "titles <id>" lists them, "play <id> <key>" prints a
// URL that curl -k or ffplay -tls_verify 0 plays through this server
// (anyone who has it may, for as long as the playback lasts: an example,
// not a viewer's page), "pause <playback>" and "resume <playback>" say
// the viewer paused or resumed, "stop <playback>" ends it. Until then the
// playback is reported to the peer every minute, which ends one it hears
// nothing of for 2 minutes. "rescan" tells the engine the folder changed.
// "rotate" moves this server to its next key; "stolen" revokes its key
// and rotates ("stolen none": without the next key, every pairing ends);
// "confirm <id>" accepts a peer's new key after comparing fingerprints.
// "address <url>" moves this server (listen there too, so it reaches
// itself); "uncallable on|off" says whether it can be called.
//
// Groups: "group-create <name>" founds one; "groups" lists them, with
// their members, the requests and joins waiting; "invite <group>" makes a
// code for it (redeemed with "redeem", accepted with "accept", or, from a
// peer already, "join-accept <group> <peer>"); "pair <group> <mail>"
// accepts a member's request, "refuse <group> <mail>" refuses it;
// "admin <group> <member>" names a member admin (the owner only), "ban
// <group> <member>" bans one; "dissolve <group>" ends the group and the
// pairings only it made;
// "group-offer <group>" shares the films with the group; "auto <group>
// on|off" accepts its members by itself; "leave <group>" leaves it.
package main

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"flag"
	"fmt"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/msfp-io/msfp-go"
	"github.com/msfp-io/msfp-go/store/jsonfile"
	"github.com/msfp-io/msfp-go/wire"
)

func main() {
	address := flag.String("address", "", "this server's address, https://host[:port]")
	listen := flag.String("listen", ":8443", "where to listen")
	data := flag.String("data", "msfp-data", "the directory of its records")
	loopback := flag.Bool("loopback", false, "allow peers on this machine")
	films := flag.String("films", "", "the folder of films to share")
	flag.Parse()
	ctx := context.Background()

	store, err := jsonfile.Open(*data)
	if err != nil {
		log.Fatal(err)
	}
	cert := selfSigned(*address)
	cfg := msfp.Config{Address: *address, Store: store, AllowLoopback: *loopback,
		Pins: []string{wire.Pin(cert.Leaf)}, Notifier: printer{}, Parts: []string{"catalog", "playback", "media", "video", "groups"}}
	if *films != "" {
		f := folder{*films}
		cfg.Catalog, cfg.Playback, cfg.Media = f, f, f
	}
	eng, err := msfp.Open(ctx, cfg)
	if err != nil {
		log.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("/api/v1/federation/", eng.FederationHandler())
	mux.Handle("/api/v1/stream/", eng.StreamHandler(nil))
	mux.HandleFunc("/watch/{id}/{rest...}", func(w http.ResponseWriter, r *http.Request) {
		p, ok := msfp.ParseMediaPath(r.PathValue("rest"))
		if !ok {
			http.NotFound(w, r)
			return
		}
		err := eng.Fetch(r.Context(), r.PathValue("id"), p, r.Header.Get("Range"), msfp.NewHTTPSink(r.Context(), w))
		switch {
		case errors.Is(err, msfp.ErrRangeNotSatisfiable):
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
		case errors.Is(err, msfp.ErrServerBusy):
			w.WriteHeader(http.StatusServiceUnavailable)
		case err != nil:
			http.NotFound(w, r)
		}
	})
	srv := &http.Server{Addr: *listen, Handler: mux, TLSConfig: &tls.Config{Certificates: []tls.Certificate{cert}}}
	go func() { log.Fatal(srv.ListenAndServeTLS("", "")) }()
	go eng.Run(ctx)

	me, _ := eng.Server(ctx)
	fmt.Println("this server:", me.Address, me.Fingerprint)
	in := bufio.NewScanner(os.Stdin)
	for fmt.Print("> "); in.Scan(); fmt.Print("> ") {
		cmd, arg, _ := strings.Cut(strings.TrimSpace(in.Text()), " ")
		switch cmd {
		case "code":
			code, _, err := eng.MakeCode(ctx, arg)
			show(code, err)
		case "redeem":
			p, err := eng.RedeemCode(ctx, arg)
			show(fmt.Sprintf("waiting for %s's operator; its fingerprint: %s", p.Name, p.Fingerprint), err)
		case "accept":
			p, err := eng.Accept(ctx, arg)
			show("paired with "+p.Name, err)
		case "end":
			show("ended", eng.End(ctx, arg))
		case "rotate":
			show("key changed; peers told", eng.Rotate(ctx, arg))
		case "stolen":
			rotated, err := eng.KeyStolen(ctx, strings.TrimPrefix(arg, "none"), arg == "none")
			show(fmt.Sprintf("key revoked; rotated to the next key: %v", rotated), err)
		case "confirm":
			p, err := eng.ConfirmRotation(ctx, arg)
			show("new key confirmed: "+p.Fingerprint, err)
		case "address":
			show("moving; peers are told once this server answers there", eng.SetAddress(ctx, arg))
		case "uncallable":
			show("can't be called: "+arg, eng.SetUncallable(ctx, arg == "on"))
		case "offer":
			show("offered", eng.SetOffers(ctx, arg, []string{"films"}))
		case "want":
			libs, err := eng.TheirLibraries(ctx, arg)
			var ids []string
			for _, l := range libs {
				ids = append(ids, l.ID)
			}
			if err == nil {
				err = eng.SetWants(ctx, arg, ids)
			}
			n, err := readNow(ctx, eng, arg, err)
			show(fmt.Sprintf("%d titles", n), err)
		case "titles":
			err := store.Read(ctx, func(tx msfp.Tx) error {
				titles, err := tx.PeerTitles(arg, "", 1000)
				for _, t := range titles {
					fmt.Println(t.Common().Key)
				}
				return err
			})
			show("", err)
		case "play":
			peer, key, _ := strings.Cut(arg, " ")
			pb, err := eng.Play(ctx, peer, msfp.PlayRequest{Title: key, Capabilities: msfp.Capabilities{
				Containers: []string{"mp4", "mkv", "webm", "ts"}, VideoCodecs: []string{"h264", "hevc", "vp9", "av1"},
				AudioCodecs: []string{"aac", "mp3", "opus", "ac3"}}})
			if err == nil {
				watches.start(ctx, eng, pb.ID)
			}
			name := map[string]string{"hls": "master.m3u8", "file": "file"}[pb.Protocol]
			show(fmt.Sprintf("%s/watch/%s/%s  (%s, %s; stop %s)", *address, pb.ID, name, pb.Method, pb.Protocol, pb.ID), err)
		case "pause", "resume":
			show(cmd+"d", watches.pause(eng, arg, cmd == "pause"))
		case "stop":
			watches.stop(arg)
			show("stopped", eng.Stop(ctx, arg, -1))
		case "rescan":
			eng.CatalogChanged()
		case "group-create":
			g, err := eng.CreateGroup(ctx, arg, msfp.GroupSettingsDefault)
			show("group "+g.ID, err)
		case "groups":
			groups, err := eng.Groups(ctx)
			for _, g := range groups {
				fmt.Printf("group %s %q %s\n", g.ID, g.Name, g.Role)
				for _, m := range g.Members {
					fmt.Printf("  member %s %q %s peer=%s\n", m.ID, m.Name, m.Role, m.Peer)
				}
				for _, r := range g.Requests {
					fmt.Printf("  request %s from %q\n", r.Mail, r.Name)
				}
				for _, j := range g.Joins {
					fmt.Printf("  join %s %q\n", j.Peer, j.Name)
				}
			}
			show("", err)
		case "invite":
			code, _, err := eng.MakeCodeWith(ctx, msfp.CodeOptions{Group: arg, Uses: 10})
			show(code, err)
		case "pair", "refuse", "join-accept", "auto", "admin", "ban":
			group, rest, _ := strings.Cut(arg, " ")
			var err error
			switch cmd {
			case "pair":
				err = eng.AcceptRequest(ctx, group, rest)
			case "refuse":
				err = eng.RefuseRequest(ctx, group, rest)
			case "join-accept":
				err = eng.AcceptJoin(ctx, group, rest)
			case "auto":
				err = eng.SetGroupAnswer(ctx, group, rest == "on")
			case "admin":
				err = eng.SetAdmin(ctx, group, rest, true)
			case "ban":
				err = eng.Ban(ctx, group, rest, "")
			}
			show("done", err)
		case "group-offer":
			show("offered", eng.SetGroupOffers(ctx, arg, []string{"films"}))
		case "leave":
			show("left", eng.LeaveGroup(ctx, arg, nil))
		case "dissolve":
			show("dissolved", eng.DissolveGroup(ctx, arg, false))
		case "peers":
			eng.PanelOpen()
			peers, err := eng.Peers(ctx)
			for _, p := range peers {
				fmt.Printf("%s  %-7s %s  %s  %s", p.ID, p.State, p.Fingerprint, p.Name, p.Address)
				if p.Pending != nil {
					fmt.Print("  new key to confirm: ", p.Pending.Fingerprint)
				}
				fmt.Println()
			}
			show("", err)
		default:
			fmt.Println("code [note] | redeem <code> | accept <id> | end <id> | peers | rotate [key] | stolen [key|none] | " +
				"confirm <id> | address <url> | uncallable on|off | offer <id> | want <id> | titles <id> | play <id> <key> | stop <playback> | rescan | " +
				"group-create <name> | groups | invite <group> | pair <group> <mail> | refuse <group> <mail> | join-accept <group> <peer> | admin <group> <member> | ban <group> <member> | group-offer <group> | auto <group> on|off | leave <group> | dissolve <group>")
		}
	}
}

// readNow reads a peer's titles, unless err.
func readNow(ctx context.Context, eng *msfp.Engine, peer string, err error) (int, error) {
	if err != nil {
		return 0, err
	}
	return eng.ReadNow(ctx, peer)
}

func show(ok string, err error) {
	if err != nil {
		fmt.Println("error:", err)
	} else if ok != "" {
		fmt.Println(ok)
	}
}

// printer prints the engine's events.
type printer struct{}

func (printer) Notify(e msfp.Event) {
	if r, ok := e.(msfp.RemotePlaybackEnded); ok {
		// Ended on the peer's side (or here): no more reports.
		watches.stop(r.Playback)
	}
	if w, ok := e.(msfp.PeerWaiting); ok {
		fmt.Printf("\n%s used your code; its fingerprint is %s. Compare, then: accept %s\n", w.Name, w.Fingerprint, w.Peer)
		return
	}
	fmt.Printf("\n%T %+v\n", e, e)
}

// selfSigned is a certificate for address's host, made at each start:
// its pin is named in this server's hellos, so peers accept it.
func selfSigned(address string) tls.Certificate {
	u, err := url.Parse(address)
	if err != nil {
		log.Fatal(err)
	}
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()),
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(365 * 24 * time.Hour)}
	if ip := net.ParseIP(u.Hostname()); ip != nil {
		tmpl.IPAddresses = []net.IP{ip}
	} else {
		tmpl.DNSNames = []string{u.Hostname()}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, pub, priv)
	if err != nil {
		log.Fatal(err)
	}
	leaf, _ := x509.ParseCertificate(der)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: priv, Leaf: leaf}
}

// The playbacks of peers' titles open here, each reported to its peer
// until stopped (MSFP playback section 3): at once, on a pause or a
// resume, and every minute (the engine sends one report per 45 seconds
// at most). A real host reports what its player says; this example has
// none, so the time played stands for the position.
var watches = &watching{open: map[string]*watch{}}

type watching struct {
	mu   sync.Mutex
	open map[string]*watch
}

type watch struct {
	stop   context.CancelFunc
	posMs  int64
	paused bool
	since  time.Time // when playing last resumed
}

func (ws *watching) start(ctx context.Context, eng *msfp.Engine, id string) {
	ctx, cancel := context.WithCancel(ctx)
	ws.mu.Lock()
	ws.open[id] = &watch{stop: cancel, since: time.Now()}
	ws.mu.Unlock()
	eng.Progress(id, 0, false)
	go func() {
		t := time.NewTicker(time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				pos, paused, ok := ws.where(id)
				if !ok {
					return
				}
				eng.Progress(id, pos, paused)
			}
		}
	}()
}

// where is a playback's position and whether it is paused.
func (ws *watching) where(id string) (int64, bool, bool) {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	w, ok := ws.open[id]
	if !ok {
		return 0, false, false
	}
	if w.paused {
		return w.posMs, true, true
	}
	return w.posMs + time.Since(w.since).Milliseconds(), false, true
}

func (ws *watching) pause(eng *msfp.Engine, id string, paused bool) error {
	pos, was, ok := ws.where(id)
	if !ok {
		return errors.New("no such playback here")
	}
	ws.mu.Lock()
	if w := ws.open[id]; w != nil && was != paused {
		w.posMs, w.paused, w.since = pos, paused, time.Now()
	}
	ws.mu.Unlock()
	eng.Progress(id, pos, paused)
	return nil
}

func (ws *watching) stop(id string) {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	if w, ok := ws.open[id]; ok {
		w.stop()
		delete(ws.open, id)
	}
}
