// SPDX-License-Identifier: AGPL-3.0-or-later
package pvp

import (
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func streakTestService(t *testing.T) (*Service, *Service) {
	t.Helper()
	dsn := os.Getenv("PVP_INTEGRATION_DSN")
	if dsn == "" {
		t.Skip("set PVP_INTEGRATION_DSN for streak DB tests")
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	writeRegistryControls(t, "", "")
	t.Setenv("PVP_DATA_DIR", t.TempDir())
	r, err := OpenRegistry(db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.Close)
	other, _ := r.Lookup(registryControlVersion)
	return r.Default(), other
}

func TestRegisteredTeamOwnershipIntegration(t *testing.T) {
	s, other := streakTestService(t)
	id, original := streakTestAccount(t, s)
	own, err := s.RegisteredTeam(id)
	if err != nil {
		t.Fatal(err)
	}
	if string(mustJSON(own.Session.Party)) != string(mustJSON(original.Session.Party)) || string(mustJSON(own.Session.Modifiers)) != string(mustJSON(original.Session.Modifiers)) {
		t.Fatal("registered party snapshot changed")
	}
	if own.Session.Seed != "" || own.Session.GameVersion != "" || own.RunID != "" || own.AuditPolicy != "" {
		t.Fatal("campaign identity exposed")
	}
	if _, err = other.RegisteredTeam(id); err == nil {
		t.Fatal("registered party leaked between versions")
	}
	if _, err = s.RegisteredTeam(make([]byte, 16)); err == nil {
		t.Fatal("another account's party returned")
	}
}
func streakTestAccount(t *testing.T, s *Service) ([]byte, Team) {
	t.Helper()
	text := randomID()
	id, _ := hex.DecodeString(text)
	if _, err := s.DB.Exec(`INSERT INTO accounts(uuid,username,hash,salt,registered) VALUES(?,?,?,?,UTC_TIMESTAMP())`, id, "pvs_"+text[:10], make([]byte, 32), make([]byte, 16)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.DB.Exec(`DELETE FROM accounts WHERE uuid=?`, id) })
	team := Team{RunID: randomID(), AuditPolicy: s.Policy(), Session: heldItemSession()}
	team.Session.GameVersion = s.GameVersion()
	if _, err := s.DB.Exec(`INSERT INTO `+s.teamsTable()+`(uuid,version,data) VALUES(?,?,?)`, id, s.GameVersion(), mustJSON(team)); err != nil {
		t.Fatal(err)
	}
	if err := s.update(id, func(st *State) error {
		st.Baseline = true
		st.Runs[team.RunID] = &Run{ID: team.RunID, Candidate: &team.Session}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return id, team
}
func streakTestResult(t *testing.T, s *Service, id []byte, m *Match, result string) {
	t.Helper()
	if _, err := s.Commit(id, m.ID, Decision{Index: 0, Kind: "turn", Digest: strings.Repeat("a", 64), Choice: json.RawMessage(`{"command":{"command":0,"cursor":0}}`)}); err != nil {
		t.Fatal(err)
	}
	player := make([]int, len(m.Session["party"].([]any)))
	enemy := make([]int, len(m.Session["enemyParty"].([]any)))
	if result == "won" {
		player[0] = 1
	} else {
		enemy[0] = 1
	}
	if _, err := s.Finish(id, m.ID, result, player, enemy, 1); err != nil {
		t.Fatal(err)
	}
}

func TestStreakLifecycleIntegration(t *testing.T) {
	s, _ := streakTestService(t)
	id, team := streakTestAccount(t, s)
	streakTestAccount(t, s)
	streakTestAccount(t, s)
	first, err := s.StartMatch(id, false)
	if err != nil {
		t.Fatal(err)
	}
	streakTestResult(t, s, id, first, "won")
	board, err := s.Leaderboard(id, 0)
	if err != nil || board.Total != 0 {
		t.Fatal("unfinished streak appeared in ranking", err)
	}
	second, err := s.StartMatch(id, false)
	if err != nil {
		t.Fatal(err)
	}
	if second.OpponentTeam == first.OpponentTeam {
		t.Fatal("same registered party matched twice")
	}
	streakTestResult(t, s, id, second, "lost")
	board, err = s.Leaderboard(id, 0)
	if err != nil || board.Total != 1 || board.Self == nil || board.Self.Wins != 1 {
		t.Fatalf("final ranking missing: %+v %v", board, err)
	}
	if _, err = s.StartMatch(id, false); err == nil {
		t.Fatal("lost party was allowed to retry")
	}
	if err = s.Register(id, team.RunID, make([]Log, 200)); err == nil {
		t.Fatal("re-registration reopened consumed party")
	}
	if _, err = s.Finish(id, second.ID, "lost", []int{0, 0, 0}, []int{1, 0, 0}, 1); err != nil {
		t.Fatal(err)
	}
	if board, err = s.Leaderboard(id, 0); err != nil || board.Total != 1 {
		t.Fatal("duplicate finish created a second record", err)
	}
	archived, err := s.RankedTeam(board.Self.ID)
	if err != nil || len(archived.Session.Modifiers) != 9 {
		t.Fatal("ranked team lost held items", err)
	}
	team.RunID = randomID()
	team.Session.Party[0] = asObject(team.Session.Party[1])
	if _, err = s.DB.Exec(`UPDATE `+s.teamsTable()+` SET data=? WHERE uuid=?`, mustJSON(team), id); err != nil {
		t.Fatal(err)
	}
	if err = s.update(id, func(st *State) error { st.StreakAttempt = nil; return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err = s.StartMatch(id, false); err != nil {
		t.Fatal("new clear party could not start", err)
	}
	archived, err = s.RankedTeam(board.Self.ID)
	if err != nil || num(asObject(archived.Session.Party[0])["species"]) != 3 {
		t.Fatal("new registration overwrote ranked party", err)
	}
}

func TestStreakExhaustionAndConcurrentStartIntegration(t *testing.T) {
	s, _ := streakTestService(t)
	id, _ := streakTestAccount(t, s)
	streakTestAccount(t, s)
	var group sync.WaitGroup
	matches := make(chan *Match, 8)
	for i := 0; i < 8; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			m, err := s.StartMatch(id, false)
			if err != nil {
				t.Error(err)
				return
			}
			matches <- m
		}()
	}
	group.Wait()
	close(matches)
	var first *Match
	for m := range matches {
		if first == nil {
			first = m
		}
		if m.ID != first.ID {
			t.Fatal("concurrent start created multiple attempts")
		}
	}
	if first == nil {
		t.Fatal("no match")
	}
	streakTestResult(t, s, id, first, "won")
	if _, err := s.StartMatch(id, false); err == nil {
		t.Fatal("exhausted opponent was reused")
	}
	streakTestAccount(t, s)
	next, err := s.StartMatch(id, false)
	if err != nil || next.OpponentTeam == first.OpponentTeam {
		t.Fatal("new opponent could not be matched", err)
	}
	if err = s.EndStreak(id); err != nil {
		t.Fatal(err)
	}
	board, err := s.Leaderboard(id, 0)
	if err != nil || board.Self.Wins != 1 {
		t.Fatal("manual end lost final wins", err)
	}
	if _, err = s.StartMatch(id, true); err == nil && !s.Practice {
		t.Fatal("disabled practice started")
	}
}

func TestStreakTimeoutAndPracticeIntegration(t *testing.T) {
	s, _ := streakTestService(t)
	id, _ := streakTestAccount(t, s)
	streakTestAccount(t, s)
	m, err := s.StartMatch(id, false)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.update(id, func(st *State) error { st.Match.Expires = time.Now().Unix() - 1; return nil }); err != nil {
		t.Fatal(err)
	}
	if err = s.finishExpiredStreaks(); err != nil {
		t.Fatal(err)
	}
	board, err := s.Leaderboard(id, 0)
	if err != nil || board.Self == nil || board.Self.Result != "timeout" || board.Self.Wins != 0 {
		t.Fatal("offline timeout not finalized", err)
	}
	if _, err = s.StartMatch(id, false); err == nil {
		t.Fatal("timed-out party could retry")
	}
	if _, err = s.Finish(id, m.ID, "forfeit", nil, nil, 0); err != nil {
		t.Fatal(err)
	}
	s.Practice = true
	practice, err := s.StartMatch(id, true)
	if err != nil {
		t.Fatal(err)
	}
	streakTestResult(t, s, id, practice, "won")
	board, err = s.Leaderboard(id, 0)
	if err != nil || board.Total != 1 || board.Self.Wins != 0 {
		t.Fatal("practice changed final ranking", err)
	}
}

func TestStreakRankingPaginationAndVersionsIntegration(t *testing.T) {
	s, other := streakTestService(t)
	var self []byte
	for i := 0; i < 6; i++ {
		id, team := streakTestAccount(t, s)
		self = id
		if err := s.update(id, func(st *State) error {
			st.Streak = 2
			st.StreakAttempt = &StreakAttempt{ID: randomID(), RunID: team.RunID, Status: "active", Wins: 2, Team: team}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if err := s.EndStreak(id); err != nil {
			t.Fatal(err)
		}
		if _, err := s.DB.Exec(`UPDATE `+s.streaksTable()+` SET endedAt=? WHERE uuid=?`, 100+i, id); err != nil {
			t.Fatal(err)
		}
	}
	page, err := s.Leaderboard(self, 0)
	if err != nil || page.Total != 6 || len(page.Entries) != 5 || page.Self.Position != 6 {
		t.Fatalf("wrong page: %+v %v", page, err)
	}
	for _, entry := range page.Entries {
		if entry.Rank != 1 {
			t.Fatal("equal scores did not share rank")
		}
	}
	last, err := s.Leaderboard(self, 99)
	if err != nil || last.Page != 1 || len(last.Entries) != 1 || !last.Entries[0].IsSelf {
		t.Fatal("last page or self marker incorrect", err)
	}
	old, err := other.Leaderboard(self, 0)
	if err != nil || old.Total != 0 {
		t.Fatal("ranking leaked between versions", err)
	}
	if _, err = other.RankedTeam(last.Entries[0].ID); err == nil {
		t.Fatal("another version's ranked party was visible")
	}
	for _, page := range []int{-1, 1000001} {
		if _, err = s.Leaderboard(self, page); err == nil {
			t.Fatal(fmt.Sprint("invalid page accepted: ", page))
		}
	}
}
