// SPDX-License-Identifier: AGPL-3.0-or-later
package pvp

import (
	"bytes"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	_ "github.com/go-sql-driver/mysql"
	"github.com/pagefaultgames/rogueserver/defs"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Uses the real local DB with routing-control exports. It does not assert an
// older native engine replay. Background recovery is disabled so existing
// users' pending jobs cannot be processed by a control verifier.
func TestRegistryStatePoolsAndUnlimitedRegistrationIntegration(t *testing.T) {
	dsn := os.Getenv("PVP_INTEGRATION_DSN")
	if dsn == "" {
		t.Skip("set PVP_INTEGRATION_DSN for registry DB integration")
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ids := [][]byte{}
	for i := 0; i < 2; i++ {
		text := randomID()
		id, _ := hex.DecodeString(text)
		if _, err = db.Exec(`INSERT INTO accounts(uuid,username,hash,salt,registered) VALUES(?,?,?,?,UTC_TIMESTAMP())`, id, "pvp_"+text[:12], make([]byte, 32), make([]byte, 16)); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
		defer db.Exec(`DELETE FROM accounts WHERE uuid=?`, id)
	}
	// The legacy table exists on production; establish it for an empty test DB.
	legacy := &Service{DB: db, profile: &VersionProfile{Version: legacyStorageVersion}}
	if err = legacy.createTables(); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	original, _ := json.Marshal(State{Best: 19, Streak: 7, Registrations: []int64{now, now}, Audit: &Audit{ID: "legacy-pending-control", Status: "pending", Logs: []Log{{Wave: 1, Payload: "preserved"}}}})
	if _, err = db.Exec(`INSERT INTO pvpAccounts(uuid,data) VALUES(?,?)`, ids[0], original); err != nil {
		t.Fatal(err)
	}
	writeRegistryControls(t, "", "")
	registry, err := OpenRegistry(db)
	if err != nil {
		t.Fatal(err)
	}
	defer registry.Close()
	var preserved []byte
	if err = db.QueryRow(`SELECT data FROM pvpAccounts WHERE uuid=?`, ids[0]).Scan(&preserved); err != nil || !bytes.Equal(preserved, original) {
		t.Fatal("opening registry changed legacy data", err)
	}
	current, ok := registry.Lookup(Version)
	if !ok {
		t.Fatal("default missing")
	}
	other, ok := registry.Lookup(registryControlVersion)
	if !ok {
		t.Fatal("control version missing")
	}
	if err = current.update(ids[0], func(st *State) error { st.Audit = nil; return nil }); err != nil {
		t.Fatal(err)
	}
	runs := map[string]*Run{}
	for _, s := range []*Service{current, other} {
		system := defs.SystemSaveData{TrainerId: 1234, SecretId: 5678, GameVersion: s.GameVersion(), StarterData: defs.StarterData{1: {}}, DexData: defs.DexData{}}
		if err = s.ObserveSystem(ids[0], system, system, nil); err != nil {
			t.Fatal(err)
		}
		run, e := s.StartRun(ids[0], s.GameVersion(), []Starter{{Species: 1, Moves: []int{33}}}, system)
		if e != nil {
			t.Fatal(e)
		}
		if run.Contract.AuditPolicy != s.Policy() || run.Version != s.GameVersion() {
			t.Fatal("run metadata crossed profiles")
		}
		runs[s.GameVersion()] = run
		tickets, e := s.EggTicketsFor(ids[0])
		if e != nil || len(tickets["tickets"].([]EggTicket)) != 64 {
			t.Fatal("version tickets missing", e)
		}
	}
	a, _ := current.Status(ids[0])
	b, _ := other.Status(ids[0])
	if a["best"] != 19 || b["best"] != 0 || len(a["runs"].(map[string]*Run)) != 1 || len(b["runs"].(map[string]*Run)) != 1 {
		t.Fatal("account progress leaked across profiles")
	}
	if err = other.update(ids[0], func(st *State) error { st.flag("control-only"); return nil }); err != nil {
		t.Fatal(err)
	}
	a, _ = current.Status(ids[0])
	if len(a["flags"].([]string)) != 0 {
		t.Fatal("qualification flag leaked")
	}
	if err = other.update(ids[0], func(st *State) error { st.Flags = nil; return nil }); err != nil {
		t.Fatal(err)
	}
	for _, s := range []*Service{current, other} {
		for _, id := range ids {
			team := PracticeTeam()
			if s == other {
				team.Session.Party[0], team.Session.Party[1] = team.Session.Party[1], team.Session.Party[0]
			}
			team.AuditPolicy = s.Policy()
			team.Session.GameVersion = s.GameVersion()
			raw, _ := json.Marshal(team)
			if _, err = db.Exec(`REPLACE INTO `+s.teamsTable()+`(uuid,version,data) VALUES(?,?,?)`, id, s.GameVersion(), raw); err != nil {
				t.Fatal(err)
			}
		}
		m, e := s.StartMatch(ids[0], false)
		if e != nil || m.Session["gameVersion"] != s.GameVersion() {
			t.Fatal("match used another game version", e)
		}
		enemy := m.Session["enemyParty"].([]any)
		want := 3
		if s == other {
			want = 6
		}
		if int(num(asObject(enemy[0])["species"])) != want {
			t.Fatal("opponent came from another pool")
		}
		if _, e = s.Finish(ids[0], m.ID, "forfeit", nil, nil, 0); e != nil {
			t.Fatal(e)
		}
	}
	// Legacy and new attempts remain recorded, without imposing a daily cap.
	for _, s := range []*Service{current, other, other} {
		if err = s.update(ids[0], func(st *State) error { st.Audit = nil; return nil }); err != nil {
			t.Fatal(err)
		}
		run := runs[s.GameVersion()]
		if err = s.update(ids[0], func(st *State) error { st.Runs[run.ID].Candidate = &defs.SessionSaveData{}; return nil }); err != nil {
			t.Fatal(err)
		}
		if err = s.Register(ids[0], run.ID, make([]Log, 200)); err != nil {
			t.Fatal(err)
		}
	}
	if err = current.update(ids[0], func(st *State) error { st.Audit = nil; return nil }); err != nil {
		t.Fatal(err)
	}
	if err = current.Register(ids[0], runs[Version].ID, make([]Log, 200)); err != nil {
		t.Fatal("sixth registration was blocked", err)
	}
	var raw []byte
	if err = db.QueryRow(`SELECT data FROM pvpRegistrationLimits WHERE uuid=?`, ids[0]).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var limit registrationLimit
	if err = json.Unmarshal(raw, &limit); err != nil || len(limit.Attempts) != 6 {
		t.Fatal("legacy attempt multiplicity was lost", err)
	}
	// Concurrent profile transactions must retain every recorded attempt.
	if _, err = db.Exec(`INSERT INTO pvpRegistrationLimits(uuid,data) VALUES(?,'{}')`, ids[1]); err != nil {
		t.Fatal(err)
	}
	var accepted atomic.Int32
	var group sync.WaitGroup
	for i := 0; i < 8; i++ {
		s := []*Service{current, other}[i%2]
		group.Add(1)
		go func(s *Service) {
			defer group.Done()
			e := s.updateTransaction(ids[1], func(st *State, tx *sql.Tx) error {
				return reserveRegistration(tx, ids[1], s.GameVersion(), time.Now().Unix(), nil)
			})
			if e == nil {
				accepted.Add(1)
			} else {
				t.Error(e)
			}
		}(s)
	}
	group.Wait()
	if accepted.Load() != 8 {
		t.Fatalf("racing profile transactions recorded %d attempts", accepted.Load())
	}
	registry.Close()
	resumed, err := OpenRegistry(db)
	if err != nil {
		t.Fatal(err)
	}
	defer resumed.Close()
	oldAgain, _ := resumed.Lookup(registryControlVersion)
	if err = oldAgain.update(ids[0], func(st *State) error { st.Audit = nil; return nil }); err != nil {
		t.Fatal(err)
	}
	if err = oldAgain.Register(ids[0], runs[registryControlVersion].ID, make([]Log, 200)); err != nil {
		t.Fatal("registration after restart was blocked", err)
	}
	state, e := oldAgain.Status(ids[0])
	if e != nil || len(state["runs"].(map[string]*Run)) != 1 {
		t.Fatal("version run was not recovered", e)
	}
}
