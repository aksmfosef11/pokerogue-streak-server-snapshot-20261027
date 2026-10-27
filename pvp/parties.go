// SPDX-License-Identifier: AGPL-3.0-or-later
package pvp

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/pagefaultgames/rogueserver/defs"
)

// Only party and item details are exposed, never campaign state or trainer IDs.
type RegisteredParty struct {
	ID       string               `json:"id"`
	Session  defs.SessionSaveData `json:"session"`
	Defense  DefenseRecord        `json:"defense"`
	CanStart bool                 `json:"canStart"`
}

func (s *Service) RegisteredParties(id []byte) ([]RegisteredParty, error) {
	type snapshot struct {
		team Team
		at   int64
	}
	teams := map[string]snapshot{}
	rows, err := s.DB.Query(`SELECT data,registeredAt FROM pvpRegisteredParties WHERE uuid=? AND version=?`, id, s.GameVersion())
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var raw []byte
		var at int64
		if err = rows.Scan(&raw, &at); err != nil {
			rows.Close()
			return nil, err
		}
		var team Team
		if err = json.Unmarshal(raw, &team); err != nil {
			rows.Close()
			return nil, err
		}
		teams[teamRunID(team)] = snapshot{team, at}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	// The pre-existing table remains authoritative for its current party.
	var raw []byte
	err = s.DB.QueryRow(`SELECT data FROM `+s.teamsTable()+` WHERE uuid=? AND version=?`, id, s.GameVersion()).Scan(&raw)
	if err != nil && err != sql.ErrNoRows {
		return nil, err
	}
	if err == nil {
		var team Team
		if err = json.Unmarshal(raw, &team); err != nil {
			return nil, err
		}
		teams[teamRunID(team)] = snapshot{team, 1 << 62}
	}
	keys := make([]string, 0, len(teams))
	for key, item := range teams {
		if item.team.AuditPolicy == s.Policy() {
			keys = append(keys, key)
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		if teams[keys[i]].at == teams[keys[j]].at {
			return keys[i] < keys[j]
		}
		return teams[keys[i]].at > teams[keys[j]].at
	})
	defense := map[string]DefenseRecord{}
	rows, err = s.DB.Query(`SELECT runId,COALESCE(SUM(result='won'),0),COALESCE(SUM(result='lost'),0) FROM pvpDefenseResults WHERE uuid=? AND version=? GROUP BY runId`, id, s.GameVersion())
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var key string
		var record DefenseRecord
		if err = rows.Scan(&key, &record.Wins, &record.Losses); err != nil {
			rows.Close()
			return nil, err
		}
		defense[key] = record
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	used := map[string]bool{}
	rows, err = s.DB.Query(`SELECT runId FROM `+s.streaksTable()+` WHERE uuid=?`, id)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var key string
		if err = rows.Scan(&key); err != nil {
			rows.Close()
			return nil, err
		}
		used[key] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	out := make([]RegisteredParty, 0, len(keys))
	for _, key := range keys {
		team := teams[key].team
		item := RegisteredParty{ID: key, Session: defs.SessionSaveData{Party: team.Session.Party, Modifiers: team.Session.Modifiers}, Defense: defense[key], CanStart: !used[key]}
		out = append(out, item)
	}
	return out, nil
}

func (s *Service) selectedParty(tx *sql.Tx, id []byte, selected string) (Team, error) {
	var team Team
	var raw []byte
	err := tx.QueryRow(`SELECT data FROM `+s.teamsTable()+` WHERE uuid=? AND version=?`, id, s.GameVersion()).Scan(&raw)
	if err != nil && err != sql.ErrNoRows {
		return team, err
	}
	if err == nil {
		if err = json.Unmarshal(raw, &team); err != nil {
			return team, err
		}
		if selected == "" || selected == teamRunID(team) {
			if team.AuditPolicy == s.Policy() {
				return team, nil
			}
			return Team{}, fmt.Errorf("등록된 파티를 선택하세요")
		}
	}
	if selected != "" {
		err = tx.QueryRow(`SELECT data FROM pvpRegisteredParties WHERE uuid=? AND version=? AND runId=?`, id, s.GameVersion(), selected).Scan(&raw)
		if err == nil {
			if err = json.Unmarshal(raw, &team); err != nil {
				return team, err
			}
			if selected == teamRunID(team) && team.AuditPolicy == s.Policy() {
				return team, nil
			}
		} else if err != sql.ErrNoRows {
			return team, err
		}
	}
	return Team{}, fmt.Errorf("등록된 파티를 선택하세요")
}
