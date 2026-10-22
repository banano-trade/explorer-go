package live

import (
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"
)

const sample = `{"topic":"confirmation","time":"1792689008405","message":{"account":"ban_3346kkobb11qqpo17imgiybmwrgibr7yi34mwn5j6uywyke8f7fnfp94uyps","amount":"2000000000000000000000000000","hash":"4712F8328EC811A02BD61373DBAE15E44EA28756575BB6406943A4B1D138C184","confirmation_type":"active_quorum","election_info":{"duration":"147","time":"1792689008404","voters":"20"},"block":{"type":"state","representative":"ban_19potasho7ozny8r1drz3u3hb3r97fw4ndm4hegdsdzzns1c3nobdastcgaa","link_as_account":"ban_1iosdm4on6bpczjhsqwecjqnpnoxwpjepe35i1qftshpmo5t69xifhd58nnn","subtype":"send"}}}`

func TestParse(t *testing.T) {
	c, ok := parse([]byte(sample))
	if !ok {
		t.Fatal("not parsed")
	}
	if c.Type != "send" || c.DurationMS != 147 || c.Voters != 20 || c.Time != 1792689008404 || c.To == "" {
		t.Fatalf("bad parse: %+v", c)
	}
	if _, ok := parse([]byte(`{"ack":"subscribe"}`)); ok {
		t.Fatal("ack parsed as confirmation")
	}
}

func TestPercentiles(t *testing.T) {
	var ds []int64
	for i := int64(1); i <= 100; i++ {
		ds = append(ds, 101-i)
	}
	m, p, x := Percentiles(ds)
	if m != 51 || p != 95 || x != 100 {
		t.Fatalf("got %d %d %d", m, p, x)
	}
	if m, p, x := Percentiles([]int64{7}); m != 7 || p != 7 || x != 7 {
		t.Fatal("single value")
	}
}

func TestFeedSaveLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "live.json")
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	f := New("", path, log)
	ch, stop, _ := f.Subscribe(0)
	defer stop()
	now := time.Now()
	for i := 0; i < 30; i++ {
		f.add(Confirmation{Hash: "H", Type: "send", Time: now.UnixMilli(), DurationMS: int64(100 + i), Voters: 20})
	}
	if len(ch) != 30 {
		t.Fatalf("subscriber got %d", len(ch))
	}
	if s := f.Stats(time.Hour); s.Count != 30 || s.Median != 115 || s.Voters != 20 {
		t.Fatalf("stats %+v", s)
	}
	if err := f.save(); err != nil {
		t.Fatal(err)
	}
	g := New("", path, log)
	if len(g.Latest(100, nil)) != 30 {
		t.Fatal("snapshot not restored")
	}
}

func TestFill(t *testing.T) {
	f := New("", "", slog.New(slog.NewTextHandler(io.Discard, nil)))
	f.add(Confirmation{Hash: "A", Type: "send", Time: time.Now().UnixMilli()})
	f.add(Confirmation{Hash: "B", Type: "send", Time: time.Now().UnixMilli(), DurationMS: 90})
	if n := f.Fill(map[string]Election{"A": {DurationMS: 120, Voters: 19}, "B": {DurationMS: 5}}); n != 1 {
		t.Fatalf("filled %d", n)
	}
	if s := f.Stats(time.Hour); s.Count != 2 || s.Max != 120 {
		t.Fatalf("stats %+v", s)
	}
}

func TestVotes(t *testing.T) {
	f := New("", "", slog.New(slog.NewTextHandler(io.Discard, nil)))
	f.listening = time.Now().Add(-time.Hour)
	rep, hs, ok := parseVote([]byte(`{"topic": "vote", "message": {"account": "ban_r1", "blocks": ["A"], "type": "vote"}}`))
	if !ok || rep != "ban_r1" || hs[0] != "A" {
		t.Fatal("parseVote")
	}
	if _, _, ok := parseVote([]byte(sample)); ok {
		t.Fatal("confirmation parsed as vote")
	}
	f.connected = true
	old := time.Now().Add(-time.Minute).UnixMilli()
	f.add(Confirmation{Hash: "A", Time: old})
	f.add(Confirmation{Hash: "B", Time: old})
	f.add(Confirmation{Hash: "C", Time: old}) // no votes seen: not counted
	f.addVote("ban_r1", []string{"A", "B"})
	f.addVote("ban_r2", []string{"A"})
	n, by := f.VoteShare()
	if n != 2 || by["ban_r1"] != 2 || by["ban_r2"] != 1 {
		t.Fatal(n, by)
	}
}

func TestVotesBeforeConfirmation(t *testing.T) {
	f := New("", "", slog.New(slog.NewTextHandler(io.Discard, nil)))
	f.listening = time.Now().Add(-time.Hour)
	f.connected = true
	old := time.Now().Add(-time.Minute).UnixMilli()
	f.addVote("ban_r1", []string{"X", "OLDBLOCK"}) // X not confirmed yet
	f.add(Confirmation{Hash: "X", Time: old})
	f.addVote("ban_r2", []string{"X"}) // a vote after confirmation
	n, by := f.VoteShare()
	if n != 1 || by["ban_r1"] != 1 || by["ban_r2"] != 1 {
		t.Fatal(n, by)
	}
	for i := 0; i < maxPending+10; i++ { // flood of unrelated hashes
		f.addVote("ban_r3", []string{fmt.Sprint("H", i)})
	}
	if n, by := f.VoteShare(); n != 1 || by["ban_r1"] != 1 {
		t.Fatal("flood wiped real votes", n, by)
	}
}

func TestVotesOnlyWhileListening(t *testing.T) {
	f := New("", "", slog.New(slog.NewTextHandler(io.Discard, nil)))
	f.listening = time.Now().Add(-2 * time.Minute)
	f.Seed([]Confirmation{{Hash: "SEEDED", Time: time.Now().Add(-time.Hour).UnixMilli()}})
	f.addVote("ban_r1", []string{"SEEDED"}) // a replay for a block from before we listened
	if n, _ := f.VoteShare(); n != 0 {
		t.Fatal("block from before listening was sampled")
	}
}

func TestVotesSurviveRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "live.json")
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	f := New("", path, log)
	f.connected, f.listening = true, time.Now().Add(-time.Hour)
	f.add(Confirmation{Hash: "A", Time: time.Now().Add(-time.Minute).UnixMilli()})
	f.addVote("ban_r1", []string{"A"})
	if err := f.save(); err != nil {
		t.Fatal(err)
	}
	g := New("", path, log)
	if n, by := g.VoteShare(); n != 1 || by["ban_r1"] != 1 {
		t.Fatal("votes lost on restart", n, by)
	}
}
