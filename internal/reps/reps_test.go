package reps

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRate(t *testing.T) {
	good := Facts{Online: true, UptimeMonth: 99.95, TrackedMins: 40000, SharePct: 0.8, Principal: true, VotePct: 98, HasNode: true, Current: true, InSync: true}
	if s := Rate(good); s.Points != 100 || s.Grade != "A" {
		t.Fatalf("%+v", s)
	}
	// banano.nano.trade in the research: 11.3% weight, 95.86% uptime -> D.
	big := Facts{Online: true, UptimeMonth: 95.86, TrackedMins: 40000, SharePct: 11.3, Principal: true, VotePct: 97, HasNode: true, Current: true, InSync: true}
	if s := Rate(big); s.Grade != "D" || s.Points != 5+25+0+15 {
		t.Fatalf("%+v", s)
	}
	small := Facts{Online: true, UptimeMonth: 100, TrackedMins: 40000, SharePct: 0.01, VotePct: -1}
	if s := Rate(small); s.Points != 35+15+25+7 || s.Grade != "B" {
		t.Fatalf("%+v", s)
	}
	shared := good
	shared.SharedHost = true
	if s := Rate(shared); s.Points != 92 {
		t.Fatalf("%+v", s)
	}
	off := good
	off.Online = false
	if Rate(off).Grade != "Offline" {
		t.Fatal("offline grade")
	}
}

func TestUptimeGraceAndOutage(t *testing.T) {
	u, _ := NewUptime("")
	a := "ban_x"
	now := time.Now()
	on := map[string]bool{a: true}
	tracked := map[string]bool{a: true}
	for i := 0; i < 100; i++ {
		u.Record(now, on, tracked)
	}
	// A 3-minute blip is forgiven.
	for i := 0; i < 3; i++ {
		u.Record(now, nil, tracked)
	}
	u.Record(now, on, tracked)
	if s := u.Stats(a); s.Day != 100 || s.LastOutage != -1 {
		t.Fatalf("blip counted: %+v", s)
	}
	// A 10-minute outage counts in full.
	for i := 0; i < 10; i++ {
		u.Record(now, nil, tracked)
	}
	for i := 0; i < 6; i++ {
		u.Record(now, on, tracked)
	}
	s := u.Stats(a)
	if s.OutageLen != 10 || s.LastOutage != 6 || s.TrackedMins != 120 {
		t.Fatalf("%+v", s)
	}
}

func TestImportSpyglass(t *testing.T) {
	dir := t.TempDir()
	doc := `{"address":"ban_177d6g6o3g8tdgqd7ia9aitx5xkrs4o776bnci1exiqstrpwztbgg368gyj8","trackStartUnixTimestamp":1762725448689,"pingStats":[{"1":100},{"0":5},{"1":95}]}`
	os.WriteFile(filepath.Join(dir, "ban_177d6g6o3g8tdgqd7ia9aitx5xkrs4o776bnci1exiqstrpwztbgg368gyj8.json"), []byte(doc), 0o644)
	u, _ := NewUptime(filepath.Join(dir, "out", "uptime.json"))
	if n, err := u.ImportSpyglass(dir); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	if s := u.Stats("ban_177d6g6o3g8tdgqd7ia9aitx5xkrs4o776bnci1exiqstrpwztbgg368gyj8"); s.Month != 97.5 {
		t.Fatalf("%+v", s)
	}
	if err := u.Save(); err != nil {
		t.Fatal(err)
	}
	v, err := NewUptime(u.Path)
	if err != nil || v.Stats("ban_177d6g6o3g8tdgqd7ia9aitx5xkrs4o776bnci1exiqstrpwztbgg368gyj8").Month != 97.5 {
		t.Fatal("reload", err)
	}
}
