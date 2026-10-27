// SPDX-License-Identifier: AGPL-3.0-or-later
package pvp

import (
	"encoding/json"
	"github.com/pagefaultgames/rogueserver/defs"
	"os"
	"strings"
	"testing"
)

func TestMinorVersionQualificationUpgradeIntegration(t *testing.T) {
	target, source := streakTestService(t)
	id, _ := streakTestAccount(t, source)
	target.profile.ClientPath = "/engines/" + target.GameVersion() + "/"
	source.profile.BattleVersion = target.GameVersion()
	system := defs.SystemSaveData{TrainerId: 1234, SecretId: 5678, GameVersion: source.GameVersion(), StarterData: defs.StarterData{1: {CandyCount: 42, EggMoves: 5, ValueReduction: 1}}, DexData: defs.DexData{1: {CaughtCount: 1}}, EggPity: []int{0, 0, 0, 0}, UnlockPity: []int{0, 0, 0, 0}}
	if err := source.update(id, func(st *State) error {
		st.Baseline = true
		st.TrustSchema = 1
		st.SystemHashSchema = 2
		st.SystemHash = systemFingerprint(system)
		st.EggTrustSchema = 1
		st.VerifiedEggMoves = map[int]int{1: 5}
		st.CurrentEggMoves = map[int]int{1: 5}
		trustStarters(st, system)
		st.EggIssued = 12
		st.EggUsed = 10
		st.TrustEpoch = 2
		st.EggJobs = []*EggAuditJob{{ID: "pending-source-job", Epoch: 2}}
		st.EggCertificates = map[int]*EggCertificate{123: {Egg: defs.EggData{Id: 123, Species: 1}, Epoch: 2, Status: "pending"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var before []byte
	source.DB.QueryRow(`SELECT data FROM `+source.accountsTable()+` WHERE uuid=?`, id).Scan(&before)
	next := system
	next.GameVersion = target.GameVersion()
	if err := target.ObserveSystem(id, system, next, nil); err != nil {
		t.Fatal(err)
	}
	var state State
	var raw []byte
	if err := target.DB.QueryRow(`SELECT data FROM `+target.accountsTable()+` WHERE uuid=?`, id).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	json.Unmarshal(raw, &state)
	if len(state.Flags) > 0 || state.MigratedFrom != source.GameVersion() || state.TrustedStarters[1].EggMoves != 5 || state.SystemHash != systemFingerprint(next) {
		t.Fatalf("trusted progress lost on upgrade: %+v", state.Flags)
	}
	if len(state.Runs) != 0 || state.StreakAttempt != nil || state.Match != nil || state.Streak != 0 {
		t.Fatal("source campaign or streak moved into new tables")
	}
	if state.EggJobs[0].SourceVersion != source.GameVersion() || state.EggCertificates[123] == nil || state.EggUsed != 12 {
		t.Fatal("pending egg provenance or ticket boundary lost")
	}
	source.DB.QueryRow(`SELECT data FROM `+source.accountsTable()+` WHERE uuid=?`, id).Scan(&raw)
	if string(raw) != string(before) {
		t.Fatal("qualification upgrade rewrote original state")
	}
}

func TestNative122EggMigrationCompensation(t *testing.T) {
	raw, err := os.ReadFile("rules.json")
	if err != nil {
		t.Fatal(err)
	}
	rules := Rules{}
	if err = json.Unmarshal(raw, &rules); err != nil {
		t.Fatal(err)
	}
	s := &Service{profile: &VersionProfile{Version: "1.12.2.0"}, Rules: rules}
	system := defs.SystemSaveData{GameVersion: "1.12.1.3", StarterData: defs.StarterData{1: {CandyCount: 42}, 4: {CandyCount: 10}}, DexData: defs.DexData{1: {HatchedCount: 80, CaughtAttr: "1"}, 4: {HatchedCount: 80, CaughtAttr: "97"}}}
	migrated := s.eggBasis(system)
	if migrated.StarterData[1].Pity[1] != 9 || migrated.StarterData[4].Pity[1] != 0 || system.StarterData[1].Pity != nil || migrated.StarterData[1].CandyCount != 42 {
		t.Fatal("native migration differs or mutates saved progress")
	}
}

func TestMinorVersionPoolIsExplicitAndPinsEngine(t *testing.T) {
	path := writeRegistryControls(t, "", "")
	raw, _ := os.ReadFile(path)
	var config versionRegistryConfig
	if err := json.Unmarshal(raw, &config); err != nil {
		t.Fatal(err)
	}
	current := config.Versions[Version]
	current.ClientPath = "/engines/" + Version + "/"
	config.Versions[Version] = current
	old := config.Versions[registryControlVersion]
	old.BattleVersion = Version
	config.Versions[registryControlVersion] = old
	if err := os.WriteFile(path, mustJSON(config), 0600); err != nil {
		t.Fatal(err)
	}
	_, profiles, err := loadVersionProfiles()
	if err != nil {
		t.Fatal(err)
	}
	services := map[string]*Service{}
	for i := range profiles {
		p := profiles[i]
		services[p.Version] = &Service{profile: &p}
	}
	registry := &Registry{services: services}
	for _, s := range services {
		s.registry = registry
	}
	if len(services[Version].battleProfiles()) != 2 {
		t.Fatal("explicit compatible minor was excluded")
	}
	m := &Match{Session: Object{}}
	services[registryControlVersion].bindBattleEngine(m)
	if m.SourceVersion != registryControlVersion || m.Session["gameVersion"] != Version || m.BattlePath != current.ClientPath {
		t.Fatal("origin and engine were conflated", m)
	}
	old.BattleVersion = "1.13.0.0"
	config.Versions[registryControlVersion] = old
	os.WriteFile(path, mustJSON(config), 0600)
	if _, _, err = loadVersionProfiles(); err == nil {
		t.Fatal("unregistered engine accepted")
	}
}

func TestMinorVersionCrossMatchAndExistingMatchIntegration(t *testing.T) {
	s, old := streakTestService(t)
	id, own := streakTestAccount(t, s)
	enemyID, enemy := streakTestAccount(t, old)
	// Both directions are isolated until the operator opts in after engine tests.
	if _, err := s.StartMatch(id, false); err == nil {
		t.Fatal("cross-version matching enabled implicitly")
	}
	s.profile.ClientPath = "/engines/" + s.GameVersion() + "/"
	old.profile.BattleVersion = s.GameVersion()
	ownBefore := string(mustJSON(own))
	enemyBefore := string(mustJSON(enemy))
	m, err := s.StartMatch(id, false)
	if err != nil {
		t.Fatal(err)
	}
	if m.OpponentVersion != old.GameVersion() || m.Session["gameVersion"] != s.GameVersion() || m.OpponentTeam != opponentTeamID(enemyID, enemy) {
		t.Fatal("wrong origin or engine", m)
	}
	if err = s.ValidateBattleEngine(id, old.GameVersion()); err == nil {
		t.Fatal("wrong replay engine accepted")
	}
	bad := Decision{Index: 0, Kind: "turn", Digest: strings.Repeat("a", 64), Choice: json.RawMessage(`{"command":{"command":0,"cursor":0}}`)}
	if _, err = s.Commit(id, m.ID, bad, old.GameVersion()); err == nil {
		t.Fatal("wrong engine committed a decision")
	}
	if _, err = s.Finish(id, m.ID, "invalid", nil, nil, 0, old.GameVersion()); err == nil {
		t.Fatal("wrong engine invalidated a match")
	}
	resumed, err := s.StartMatch(id, false)
	if err != nil || resumed.ID != m.ID || len(resumed.Decisions) != 0 {
		t.Fatal("wrong engine changed pending match", err)
	}
	// Changing future pool settings must not re-label or replace an active match.
	old.profile.BattleVersion = old.GameVersion()
	resumed, err = s.StartMatch(id, false)
	if err != nil || string(mustJSON(resumed)) != string(mustJSON(m)) {
		t.Fatal("pool update altered an existing match", err)
	}
	old.profile.BattleVersion = s.GameVersion()
	streakTestResult(t, s, id, m, "won")
	if _, err = s.StartMatch(id, false); err == nil {
		t.Fatal("cross-version opponent repeated")
	}
	var raw []byte
	for _, item := range []struct {
		s    *Service
		id   []byte
		want string
	}{{s, id, ownBefore}, {old, enemyID, enemyBefore}} {
		if err = item.s.DB.QueryRow(`SELECT data FROM `+item.s.teamsTable()+` WHERE uuid=?`, item.id).Scan(&raw); err != nil || string(raw) != item.want {
			t.Fatal("registered source party changed", err)
		}
	}
	reverse, err := old.StartMatch(enemyID, false)
	if err != nil || reverse.Session["gameVersion"] != s.GameVersion() || reverse.SourceVersion != old.GameVersion() {
		t.Fatal("reverse matching failed", err)
	}
	if err = old.ValidateBattleEngine(enemyID, s.GameVersion()); err != nil {
		t.Fatal(err)
	}
	if err = old.EndStreak(enemyID); err != nil {
		t.Fatal(err)
	}
	// The old policy must still be rejected inside an otherwise shared pool.
	stale := enemy
	stale.AuditPolicy = old.GameVersion() + ":generation-obsolete"
	old.DB.Exec(`UPDATE `+old.teamsTable()+` SET data=? WHERE uuid=?`, mustJSON(stale), enemyID)
	newerID, _ := streakTestAccount(t, s)
	// Exclude the current source party, leaving only the stale foreign policy.
	s.DB.Exec(`UPDATE accounts SET banned=1 WHERE uuid=?`, id)
	if _, err = s.StartMatch(newerID, false); err == nil {
		t.Fatal("stale foreign audit policy entered shared pool")
	}
}
