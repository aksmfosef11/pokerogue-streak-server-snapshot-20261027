// SPDX-License-Identifier: AGPL-3.0-or-later
package pvp

import (
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/pagefaultgames/rogueserver/defs"
)

type DefenseRecord struct {
	Wins   int `json:"wins"`
	Losses int `json:"losses"`
}

// Match snapshots already contain the defender and run identity, including for
// battles that were active before this table was introduced. The event and the
// attacker's finished match commit together, and matchId prevents duplicates.
func recordDefense(tx *sql.Tx, m *Match, source, result string) error {
	parts := strings.SplitN(m.OpponentTeam, ":", 2)
	if len(parts) != 2 || parts[1] == "" || len(parts[1]) > 64 {
		return nil // Older snapshots without a reliable defender cannot be attributed.
	}
	id, err := hex.DecodeString(parts[0])
	if err != nil || len(id) != 16 {
		return nil
	}
	if m.OpponentVersion != "" {
		source = m.OpponentVersion
	}
	defenseResult := "won"
	if result == "won" {
		defenseResult = "lost"
	}
	// A deleted account need not prevent the attacker from completing a match.
	_, err = tx.Exec(`INSERT IGNORE INTO pvpDefenseResults(matchId,uuid,version,runId,result)
		SELECT ?,uuid,?,?,? FROM accounts WHERE uuid=?`, m.ID, source, parts[1], defenseResult, id)
	return err
}

func (s *Service) RegisteredDefense(id []byte) (DefenseRecord, error) {
	var record DefenseRecord
	var raw []byte
	err := s.DB.QueryRow(`SELECT data FROM `+s.teamsTable()+` WHERE uuid=? AND version=? AND JSON_VALUE(data,'$.auditPolicy')=?`, id, s.GameVersion(), s.Policy()).Scan(&raw)
	if err != nil {
		return record, err
	}
	var team Team
	if err = json.Unmarshal(raw, &team); err != nil {
		return record, err
	}
	err = s.DB.QueryRow(`SELECT COALESCE(SUM(result='won'),0),COALESCE(SUM(result='lost'),0)
		FROM pvpDefenseResults WHERE uuid=? AND version=? AND runId=?`, id, s.GameVersion(), teamRunID(team)).Scan(&record.Wins, &record.Losses)
	return record, err
}

func (s *Service) AttemptTeam(id []byte) (*Team, error) {
	var team *Team
	err := s.update(id, func(st *State) error {
		if st.StreakAttempt == nil || st.StreakAttempt.Status != "active" {
			return fmt.Errorf("진행 중인 연승전이 없습니다")
		}
		session := st.StreakAttempt.Team.Session
		team = &Team{Session: defs.SessionSaveData{Party: session.Party, Modifiers: session.Modifiers}}
		return nil
	})
	return team, err
}
