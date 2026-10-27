// SPDX-License-Identifier: AGPL-3.0-or-later
package pvp

import (
	"encoding/json"
	"testing"
)

func TestDefenseAndUnifiedRankingIntegration(t *testing.T) {
	current, old := streakTestService(t)
	old.profile.BattleVersion = current.GameVersion()
	a, own := streakTestAccount(t, current)
	b, _ := streakTestAccount(t, old)
	m, err := current.StartMatch(a, false)
	if err != nil {
		t.Fatal(err)
	}
	active, err := current.AttemptTeam(a)
	if err != nil || string(mustJSON(active.Session.Party)) != string(mustJSON(NormalizeParty(own.Session.Party, true))) || active.RunID != "" {
		t.Fatal("active party snapshot or privacy incorrect", err)
	}
	streakTestResult(t, current, a, m, "won")
	if _, err := current.Finish(a, m.ID, "won", nil, nil, 0); err != nil {
		t.Fatal(err)
	}
	record, err := old.RegisteredDefense(b)
	if err != nil || record.Wins != 0 || record.Losses != 1 {
		t.Fatalf("defense loss counted incorrectly: %+v %v", record, err)
	}
	if err := current.EndStreak(a); err != nil {
		t.Fatal(err)
	}
	board, err := old.Leaderboard(b, 0)
	if err != nil || board.Total != 1 || board.Entries[0].Wins != 1 {
		t.Fatalf("unified board: %+v %v", board, err)
	}
	if _, err := old.RankedTeam(board.Entries[0].ID); err != nil {
		t.Fatal("cross-profile ranking details", err)
	}
	if _, err := current.AttemptTeam(a); err == nil {
		t.Fatal("ended attempt returned as active")
	}

	reverse, err := old.StartMatch(b, false)
	if err != nil {
		t.Fatal(err)
	}
	streakTestResult(t, old, b, reverse, "lost")
	record, err = current.RegisteredDefense(a)
	if err != nil || record.Wins != 1 || record.Losses != 0 {
		t.Fatalf("defense win counted incorrectly: %+v %v", record, err)
	}

	// Registering a new party must not transfer the previous party's record.
	newTeam := own
	newTeam.RunID = randomID()
	raw, _ := json.Marshal(newTeam)
	if _, err := current.DB.Exec(`UPDATE `+current.teamsTable()+` SET data=? WHERE uuid=?`, raw, a); err != nil {
		t.Fatal(err)
	}
	record, err = current.RegisteredDefense(a)
	if err != nil || record.Wins != 0 || record.Losses != 0 {
		t.Fatalf("record leaked to new party: %+v %v", record, err)
	}
}

func TestForfeitDoesNotCountDefenseIntegration(t *testing.T) {
	s, _ := streakTestService(t)
	a, _ := streakTestAccount(t, s)
	b, _ := streakTestAccount(t, s)
	m, err := s.StartMatch(a, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Finish(a, m.ID, "forfeit", nil, nil, 0); err != nil {
		t.Fatal(err)
	}
	record, err := s.RegisteredDefense(b)
	if err != nil || record.Wins != 0 || record.Losses != 0 {
		t.Fatalf("forfeit counted: %+v %v", record, err)
	}
}
