// SPDX-License-Identifier: AGPL-3.0-or-later
package pvp

import (
	"database/sql"
	"encoding/hex"
	"encoding/json"
	_ "github.com/go-sql-driver/mysql"
	"github.com/pagefaultgames/rogueserver/defs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"
)

// Opt-in test against the local Compose database and real isolated Node engine.
// It inserts and removes only its randomly named pvp_ test account.
func TestAuditDurableRecoveryIntegration(t *testing.T) {
	dsn := os.Getenv("PVP_INTEGRATION_DSN")
	if dsn == "" {
		t.Skip("set PVP_INTEGRATION_DSN and PVP_AUDIT_FIXTURE for Docker integration")
	}
	database, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var fixture struct {
		Run  *Run  `json:"run"`
		Logs []Log `json:"logs"`
	}
	data, err := os.ReadFile(os.Getenv("PVP_AUDIT_FIXTURE"))
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.Run == nil || len(fixture.Logs) != 200 {
		t.Fatal("incomplete generation fixture")
	}
	idHex := randomID()
	id, _ := hex.DecodeString(idHex)
	name := "pvp_" + idHex[:12]
	if _, err = database.Exec(`INSERT INTO accounts(uuid,username,hash,salt,registered) VALUES(?,?,?, ?,UTC_TIMESTAMP())`, id, name, make([]byte, 32), make([]byte, 16)); err != nil {
		t.Fatal(err)
	}
	defer database.Exec(`DELETE FROM accounts WHERE uuid=?`, id)
	version := fixture.Run.Version
	rulesDir, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	if version != Version {
		rulesDir = filepath.Join(rulesDir, "versions", version)
	}
	policy := version + AuditPolicy[len(Version):]
	registryPath := filepath.Join(t.TempDir(), "versions.json")
	open := func() (*Service, error) {
		config := versionRegistryConfig{DefaultVersion: version, Versions: map[string]versionProfileConfig{
			version: {RulesDir: rulesDir, AuditPolicy: policy, AuditURL: os.Getenv("PVP_AUDIT_URL")},
		}}
		raw, e := json.Marshal(config)
		if e != nil {
			return nil, e
		}
		if e = os.WriteFile(registryPath, raw, 0600); e != nil {
			return nil, e
		}
		t.Setenv("PVP_VERSION_REGISTRY", registryPath)
		return New(database)
	}
	realURL := os.Getenv("PVP_AUDIT_URL")
	if realURL == "" {
		t.Fatal("PVP_AUDIT_URL must identify the real Compose worker")
	}
	t.Setenv("PVP_AUDIT_URL", "http://127.0.0.1:1")
	first, err := open()
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	accountsTable, teamsTable := first.accountsTable(), first.teamsTable()
	r := fixture.Run
	r.ID = "durable-integration"
	last, err := DecodeLog(fixture.Logs[199].Payload)
	if err != nil {
		t.Fatal(err)
	}
	var frame struct {
		Generation struct {
			Basis defs.SessionSaveData `json:"basis"`
		} `json:"generation"`
	}
	if err = json.Unmarshal(last, &frame); err != nil {
		t.Fatal(err)
	}
	firstFrame, err := DecodeLog(fixture.Logs[0].Payload)
	if err != nil {
		t.Fatal(err)
	}
	var initial struct {
		Generation struct {
			Basis defs.SessionSaveData `json:"basis"`
		} `json:"generation"`
	}
	if err = json.Unmarshal(firstFrame, &initial); err != nil {
		t.Fatal(err)
	}
	// The fixture skips combat. Give it plausible observed arrival times while
	// still checking every replay clock against the server's run/anchor bounds.
	r.Created = int64(initial.Generation.Basis.Timestamp) / 1000
	r.ClearedAt = max(r.Created+200, int64(frame.Generation.Basis.Timestamp)/1000)
	candidate := frame.Generation.Basis
	candidate.Seed = r.Seed
	candidate.WaveIndex = 200
	candidate.BattleType = 2
	r.Candidate = &candidate
	r.Anchors = []Anchor{{Wave: 5, Head: fixture.Logs[4].Head, At: r.Created + 5}, {Wave: 200, Head: fixture.Logs[199].Head, At: r.ClearedAt}}
	wrongDate := *r
	wrongDate.Created += 86400
	wrongDate.ClearedAt += 86400
	if ValidateLogs(&wrongDate, fixture.Logs) == nil {
		t.Fatal("actual generation proof accepted an unobserved event date")
	}
	if err = first.update(id, func(st *State) error { st.Baseline = true; st.Runs[r.ID] = r; st.Streak = 7; return nil }); err != nil {
		t.Fatal(err)
	}
	if err = first.Register(id, r.ID, fixture.Logs); err != nil {
		t.Fatal(err)
	}
	readState := func() State {
		var raw []byte
		var state State
		if err := database.QueryRow(`SELECT data FROM `+accountsTable+` WHERE uuid=?`, id).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &state); err != nil {
			t.Fatal(err)
		}
		return state
	}
	wait := func(timeout time.Duration, predicate func(State) bool) State {
		deadline := time.Now().Add(timeout)
		for time.Now().Before(deadline) {
			state := readState()
			if predicate(state) {
				return state
			}
			time.Sleep(100 * time.Millisecond)
		}
		state := readState()
		if state.Audit == nil {
			t.Fatal("audit state missing")
		}
		t.Fatalf("audit state did not converge: status=%s reason=%q attempts=%d next=%d logs=%d", state.Audit.Status, state.Audit.Reason, state.Audit.Attempts, state.Audit.NextAttempt, len(state.Audit.Logs))
		return State{}
	}
	pending := wait(10*time.Second, func(st State) bool { return st.Audit != nil && st.Audit.Attempts > 0 && st.Audit.Reason != "" })
	if pending.Audit.Status != "pending" || len(pending.Audit.Logs) != 200 {
		t.Fatal("worker outage lost/approved pending proof")
	}
	first.Close()
	// Make the persisted retry due immediately instead of waiting the tested
	// exponential backoff. New() must discover it with no in-memory queue entry.
	if _, err = database.Exec(`UPDATE `+accountsTable+` SET data=JSON_SET(data,'$.audit.nextAttempt',0) WHERE uuid=?`, id); err != nil {
		t.Fatal(err)
	}
	// A still-running previous worker during deployment must leave this same
	// persisted job pending, instead of approving or permanently rejecting it.
	for _, legacyResponse := range []struct{ name, body string }{
		{"missing proof", `{"ok":true,"partyVerified":true,"inventoryVerified":true,"formItemsVerified":true}`},
		{"missing money proof", `{"ok":true,"policy":"` + policy + `","partyVerified":true,"inventoryVerified":true,"formItemsVerified":true,"levelsVerified":true}`},
		{"old policy approval", `{"ok":true,"policy":"` + version + `:generation-v14","partyVerified":true,"inventoryVerified":true,"formItemsVerified":true,"levelsVerified":true}`},
		{"old policy rejection", `{"ok":false,"policy":"` + version + `:generation-v14","reason":"unsupported old worker input"}`},
	} {
		var legacyRequests atomic.Int32
		oldWorker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			legacyRequests.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(legacyResponse.body))
		}))
		defer oldWorker.Close()
		t.Setenv("PVP_AUDIT_URL", oldWorker.URL)
		stale, err := open()
		if err != nil {
			t.Fatal(err)
		}
		defer stale.Close()
		legacy := wait(10*time.Second, func(st State) bool {
			return legacyRequests.Load() > 0 && st.Audit != nil && st.Audit.Attempts > pending.Audit.Attempts && st.Audit.Reason != ""
		})
		if legacy.Audit.Status != "pending" || len(legacy.Audit.Logs) != 200 {
			t.Fatalf("%s lost/approved the pending proof", legacyResponse.name)
		}
		stale.Close()
		oldWorker.Close()
		pending = legacy
		t.Logf("persisted proof retained after %s", legacyResponse.name)
		if _, err = database.Exec(`UPDATE `+accountsTable+` SET data=JSON_SET(data,'$.audit.nextAttempt',0) WHERE uuid=?`, id); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PVP_AUDIT_URL", realURL)
	resumed, err := open()
	if err != nil {
		t.Fatal(err)
	}
	defer resumed.Close()
	final := wait(150*time.Second, func(st State) bool { return st.Audit != nil && st.Audit.Status != "pending" })
	if final.Audit.Status != "approved" {
		t.Fatalf("real recovered audit rejected: %+v", final.Audit)
	}
	if len(final.Audit.Logs) != 0 || final.Streak != 0 || final.Audit.ElapsedMS <= 0 {
		t.Fatal("approval state was not published")
	}
	var raw []byte
	var team Team
	if err = database.QueryRow(`SELECT data FROM `+teamsTable+` WHERE uuid=?`, id).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &team); err != nil {
		t.Fatal(err)
	}
	if team.AuditPolicy != policy || team.RunID != r.ID || len(team.Session.Party) == 0 {
		t.Fatal("missing certified team")
	}
	for i, source := range candidate.Party {
		for _, field := range []string{"customPokemonData", "fusionCustomPokemonData"} {
			custom := asObject(asObject(source)[field])
			registered := asObject(asObject(team.Session.Party[i])[field])
			if types, ok := custom["types"].([]any); ok && len(types) > 0 && !reflect.DeepEqual(types, registered["types"]) {
				t.Fatal("certified permanent types lost while publishing the team")
			}
			if ability, ok := custom["ability"].(float64); ok && ability >= 0 && !reflect.DeepEqual(ability, registered["ability"]) {
				t.Fatal("certified permanent ability lost while publishing the team")
			}
		}
	}
	// Exercise the real SQL match pool with the certified snapshot. Stale
	// versions and policies must never become an opponent in this pool.
	opponentID, _ := hex.DecodeString(randomID())
	opponentName := "pvp_" + randomID()[:12]
	if _, err = database.Exec(`INSERT INTO accounts(uuid,username,hash,salt,registered) VALUES(?,?,?, ?,UTC_TIMESTAMP())`, opponentID, opponentName, make([]byte, 32), make([]byte, 16)); err != nil {
		t.Fatal(err)
	}
	defer database.Exec(`DELETE FROM accounts WHERE uuid=?`, opponentID)
	if _, err = database.Exec(`INSERT INTO `+teamsTable+`(uuid,version,data) VALUES(?,?,?)`, opponentID, "unsupported-version", raw); err != nil {
		t.Fatal(err)
	}
	checkExcluded := func() {
		m, e := resumed.StartMatch(id, false)
		if e != nil {
			return // No other certified account is required in a local stack.
		}
		if m.Opponent == opponentName {
			t.Fatal("unsupported opponent entered the match pool")
		}
		if _, e = resumed.Finish(id, m.ID, "forfeit", nil, nil, 0); e != nil {
			t.Fatal(e)
		}
	}
	checkExcluded()
	oldTeam := team
	oldTeam.AuditPolicy = "retired"
	oldRaw, _ := json.Marshal(oldTeam)
	if _, err = database.Exec(`UPDATE `+teamsTable+` SET version=?,data=? WHERE uuid=?`, version, oldRaw, opponentID); err != nil {
		t.Fatal(err)
	}
	checkExcluded()
	if _, err = database.Exec(`UPDATE `+teamsTable+` SET data=? WHERE uuid=?`, raw, opponentID); err != nil {
		t.Fatal(err)
	}
	match, err := resumed.StartMatch(id, false)
	if err != nil || match.Practice || match.Opponent == "" {
		t.Fatalf("certified registered-party match failed: %v", err)
	}
	if match.Session["gameVersion"] != version {
		t.Fatal("registered match used another native version")
	}
	for _, side := range []string{"party", "enemyParty"} {
		party, ok := match.Session[side].([]any)
		if !ok || len(party) != len(candidate.Party) {
			t.Fatalf("registered match has no %s", side)
		}
		for i, source := range candidate.Party {
			for _, field := range []string{"customPokemonData", "fusionCustomPokemonData"} {
				custom := asObject(asObject(source)[field])
				loaded := asObject(asObject(party[i])[field])
				if types, ok := custom["types"].([]any); ok && len(types) > 0 && !reflect.DeepEqual(types, loaded["types"]) {
					t.Fatalf("certified permanent types lost in %s match session", side)
				}
				if ability, ok := custom["ability"].(float64); ok && ability >= 0 && !reflect.DeepEqual(ability, loaded["ability"]) {
					t.Fatalf("certified permanent ability lost in %s match session", side)
				}
			}
		}
	}
	if _, err = resumed.Finish(id, match.ID, "forfeit", nil, nil, 0); err != nil {
		t.Fatal(err)
	}
	if got := readState(); got.Match.Status != "forfeit" || got.Streak != 0 {
		t.Fatal("registered-party match result was not persisted")
	}
	locked := readState().Runs[r.ID]
	overwrite := candidate
	overwrite.Money++
	if err = resumed.ObserveClear(id, overwrite); err != nil {
		t.Fatal(err)
	}
	if after := readState().Runs[r.ID]; after.ClearedAt != locked.ClearedAt || after.Candidate.Money != locked.Candidate.Money {
		t.Fatal("repeated clear overwrote the server-observed candidate/time")
	}
	t.Logf("recovered %d-wave audit: %d ms, %d KiB peak RSS", len(fixture.Logs), final.Audit.ElapsedMS, final.Audit.MaxRSS)
	// A reviewed baseline is a separate, recorded qualification epoch. It must
	// preserve ordinary data while retiring prior certification and old runs.
	system := defs.SystemSaveData{TrainerId: 1234, SecretId: 5678, GameVersion: version, StarterData: defs.StarterData{1: {CandyCount: 100}}, DexData: defs.DexData{}}
	if err = resumed.update(id, func(st *State) error { st.flag("imported-baseline-unverified"); return nil }); err != nil {
		t.Fatal(err)
	}
	if err = resumed.ApproveBaseline(id, system, "integration-operator", ""); err == nil {
		t.Fatal("approval without justification accepted")
	}
	report, err := resumed.ReviewBaseline(id, system)
	if err != nil || len(report["flags"].([]string)) != 1 {
		t.Fatal("review did not expose held qualification")
	}
	if err = resumed.ApproveBaseline(id, system, "integration-operator", "reviewed test import"); err != nil {
		t.Fatal(err)
	}
	reviewed := readState()
	if len(reviewed.Flags) != 0 || reviewed.TrustEpoch != 1 || len(reviewed.Reviews) != 1 || reviewed.SystemHashSchema != 2 {
		t.Fatal("baseline review was not recorded")
	}
	if err = resumed.Register(id, r.ID, fixture.Logs); err == nil {
		t.Fatal("pre-review run retained registration eligibility")
	}
	if err = database.QueryRow(`SELECT data FROM `+teamsTable+` WHERE uuid=?`, id).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &team); err != nil || team.AuditPolicy != "retired" {
		t.Fatal("old team certification was not retired")
	}
	if _, err = resumed.StartRun(id, version, []Starter{{Species: 1, Moves: []int{33}}}, system); err != nil {
		t.Fatalf("reviewed account could not start a fresh ranked run: %v", err)
	}
}
