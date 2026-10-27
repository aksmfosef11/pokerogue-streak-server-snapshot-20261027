// SPDX-License-Identifier: AGPL-3.0-or-later
package pvp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/pagefaultgames/rogueserver/defs"
)

func TestAutomaticRegistrationKeepsActiveAttemptAndMultipleParties(t *testing.T) {
	path := os.Getenv("PVP_AUDIT_FIXTURE")
	if path == "" {
		t.Skip("PVP_AUDIT_FIXTURE supplies a real 200-wave journal")
	}
	s, _ := streakTestService(t)
	id, original := streakTestAccount(t, s)
	streakTestAccount(t, s)
	streakTestAccount(t, s)
	first, err := s.StartMatch(id, false, original.RunID)
	if err != nil {
		t.Fatal(err)
	}
	before, err := s.AttemptTeam(id)
	if err != nil {
		t.Fatal(err)
	}

	var fixture struct {
		Run  *Run  `json:"run"`
		Logs []Log `json:"logs"`
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.Run.Version != s.GameVersion() || len(fixture.Logs) != 200 {
		t.Fatal("wrong fixture")
	}
	firstRaw, err := DecodeLog(fixture.Logs[0].Payload)
	if err != nil {
		t.Fatal(err)
	}
	lastRaw, err := DecodeLog(fixture.Logs[199].Payload)
	if err != nil {
		t.Fatal(err)
	}
	var initial, final struct {
		Generation struct {
			Basis defs.SessionSaveData `json:"basis"`
		} `json:"generation"`
	}
	if err = json.Unmarshal(firstRaw, &initial); err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(lastRaw, &final); err != nil {
		t.Fatal(err)
	}
	run := fixture.Run
	run.ID = randomID()
	run.Created = int64(initial.Generation.Basis.Timestamp) / 1000
	run.ClearedAt = max(run.Created+200, int64(final.Generation.Basis.Timestamp)/1000)
	candidate := final.Generation.Basis
	candidate.Seed, candidate.WaveIndex, candidate.BattleType = run.Seed, 200, 2
	run.Candidate = &candidate
	run.Anchors = []Anchor{{Wave: 5, Head: fixture.Logs[4].Head, At: run.Created + 5}, {Wave: 200, Head: fixture.Logs[199].Head, At: run.ClearedAt}}
	if err = s.update(id, func(st *State) error { st.Runs[run.ID] = run; return nil }); err != nil {
		t.Fatal(err)
	}
	if err = s.Register(id, run.ID, fixture.Logs); err != nil {
		t.Fatal(err)
	}
	if err = s.Register(id, run.ID, fixture.Logs); err != nil {
		t.Fatal("lost response retry", err)
	}
	var attempts int
	if err = s.DB.QueryRow(`SELECT JSON_LENGTH(data,'$.attempts') FROM pvpRegistrationLimits WHERE uuid=?`, id).Scan(&attempts); err != nil || attempts != 1 {
		t.Fatalf("retry consumed the registration limit twice: %d %v", attempts, err)
	}
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(AuditResult{OK: true, Policy: s.Policy(), PartyVerified: true, InventoryVerified: true, FormItemsVerified: true, LevelsVerified: true, MoneyVerified: true})
	}))
	defer worker.Close()
	s.auditURL = worker.URL
	if err = s.auditOne(id); err != nil {
		t.Fatal(err)
	}
	status, err := s.Status(id)
	if err != nil {
		t.Fatal(err)
	}
	if status["audit"].(*Audit).Status != "approved" {
		t.Fatalf("new party not approved while match active: %+v", status["audit"])
	}
	after, err := s.AttemptTeam(id)
	if err != nil || string(mustJSON(after.Session.Party)) != string(mustJSON(before.Session.Party)) {
		t.Fatal("active party was changed", err)
	}
	parties, err := s.RegisteredParties(id)
	if err != nil || len(parties) != 2 {
		t.Fatalf("both parties not retained: %d %v", len(parties), err)
	}
	eligible := map[string]bool{}
	for _, party := range parties {
		eligible[party.ID] = party.CanStart
	}
	if eligible[original.RunID] || !eligible[run.ID] {
		t.Fatal("party attempt availability incorrect", eligible)
	}
	streakTestResult(t, s, id, first, "won")
	second, err := s.StartMatch(id, false, run.ID)
	if err != nil {
		t.Fatal("could not continue original party", err)
	}
	after, _ = s.AttemptTeam(id)
	if string(mustJSON(after.Session.Party)) != string(mustJSON(before.Session.Party)) {
		t.Fatal("selected new party replaced active attempt")
	}
	streakTestResult(t, s, id, second, "lost")
	next, err := s.StartMatch(id, false, run.ID)
	if err != nil {
		t.Fatal("new party could not start after old streak ended", err)
	}
	selected, err := s.AttemptTeam(id)
	if err != nil || string(mustJSON(selected.Session.Party)) != string(mustJSON(NormalizeParty(candidate.Party, true))) {
		t.Fatal("wrong party selected", err)
	}
	if next.ID == first.ID {
		t.Fatal("new match reused old ID")
	}
}

func TestExistingPartyCanStartWhileNewPartyIsBeingAudited(t *testing.T) {
	s, _ := streakTestService(t)
	id, existing := streakTestAccount(t, s)
	streakTestAccount(t, s)
	candidateID := randomID()
	if err := s.update(id, func(st *State) error {
		st.Runs[candidateID] = &Run{ID: candidateID, Candidate: &existing.Session}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Register(id, candidateID, make([]Log, 200)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StartMatch(id, false, existing.RunID); err != nil {
		t.Fatal("pending registration blocked an approved party", err)
	}
}

func TestBaselineResetRetiresEveryRegisteredParty(t *testing.T) {
	s, _ := streakTestService(t)
	id, first := streakTestAccount(t, s)
	second := first
	second.RunID = randomID()
	if _, err := s.DB.Exec(`INSERT INTO pvpRegisteredParties(uuid,version,runId,data,registeredAt) VALUES(?,?,?,?,1)`, id, s.GameVersion(), second.RunID, mustJSON(second)); err != nil {
		t.Fatal(err)
	}
	system := defs.SystemSaveData{GameVersion: s.GameVersion(), TrainerId: 1234, SecretId: 5678, StarterData: defs.StarterData{1: {CandyCount: 100}}, DexData: defs.DexData{}}
	if err := s.ApproveBaseline(id, system, "test-operator", "reviewed fixture"); err != nil {
		t.Fatal(err)
	}
	parties, err := s.RegisteredParties(id)
	if err != nil || len(parties) != 0 {
		t.Fatal("baseline reset left a certified party", err)
	}
	tx, err := s.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = s.selectedParty(tx, id, second.RunID); err == nil {
		t.Fatal("retired archive could still be selected")
	}
}

func TestDifferentRegisteredPartiesAreDifferentOpponents(t *testing.T) {
	s, _ := streakTestService(t)
	a, _ := streakTestAccount(t, s)
	b, old := streakTestAccount(t, s)
	newTeam := old
	newTeam.RunID = randomID()
	if _, err := s.DB.Exec(`INSERT INTO pvpRegisteredParties(uuid,version,runId,data,registeredAt) VALUES(?,?,?,?,1)`, b, s.GameVersion(), old.RunID, mustJSON(old)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`UPDATE `+s.teamsTable()+` SET data=? WHERE uuid=?`, mustJSON(newTeam), b); err != nil {
		t.Fatal(err)
	}
	first, err := s.StartMatch(a, false)
	if err != nil {
		t.Fatal(err)
	}
	streakTestResult(t, s, a, first, "won")
	second, err := s.StartMatch(a, false)
	if err != nil || second.OpponentTeam == first.OpponentTeam {
		t.Fatal("separate parties did not enter opponent pool", err)
	}
}
