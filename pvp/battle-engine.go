// SPDX-License-Identifier: AGPL-3.0-or-later
package pvp

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

func (s *Service) HasLegacyBattle(id []byte, target string) (bool, error) {
	var raw []byte
	if err := s.DB.QueryRow(`SELECT data FROM `+s.accountsTable()+` WHERE uuid=?`, id).Scan(&raw); err != nil {
		if err == sql.ErrNoRows {
			return false, nil
		}
		return false, err
	}
	var state State
	if err := json.Unmarshal(raw, &state); err != nil {
		return false, err
	}
	m := state.Match
	return m != nil && m.Status == "active" && m.Expires >= time.Now().Unix() && m.Session["gameVersion"] != target, nil
}

// Existing matches retain their serialized engine even when the registry changes.
// Do not run update/expire here: the wrong engine must not change durable state.
func (s *Service) ValidateBattleEngine(id []byte, engine string) error {
	var raw []byte
	if err := s.DB.QueryRow(`SELECT data FROM `+s.accountsTable()+` WHERE uuid=?`, id).Scan(&raw); err != nil {
		if err == sql.ErrNoRows {
			return nil
		}
		return err
	}
	var state State
	if err := json.Unmarshal(raw, &state); err != nil {
		return err
	}
	if state.Match != nil && state.Match.Status == "active" && state.Match.Session["gameVersion"] != engine {
		return fmt.Errorf("진행 중인 대전의 전투 버전으로 접속하세요")
	}
	return nil
}
