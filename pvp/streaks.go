// SPDX-License-Identifier: AGPL-3.0-or-later
package pvp

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/pagefaultgames/rogueserver/defs"
)

// The immutable registered team, not the last battle's damaged party, owns one attempt.
type StreakAttempt struct {
	ID        string   `json:"id"`
	RunID     string   `json:"runId"`
	Status    string   `json:"status"`
	Wins      int      `json:"wins"`
	StartedAt int64    `json:"startedAt"`
	EndedAt   int64    `json:"endedAt"`
	Opponents []string `json:"opponents"`
	Team      Team     `json:"team"`
}

func (s *Service) streaksTable() string { return "pvpStreaks" + s.storageSuffix() }

func (s *Service) finishExpiredStreaks() error {
	rows, err := s.DB.Query(`SELECT uuid FROM `+s.accountsTable()+` WHERE JSON_VALUE(data,'$.match.status')='active' AND CAST(JSON_VALUE(data,'$.match.expires') AS SIGNED)<?`, time.Now().Unix())
	if err != nil {
		return err
	}
	var ids [][]byte
	for rows.Next() {
		var id []byte
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err = s.update(id, func(st *State) error { return nil }); err != nil {
			return err
		}
	}
	return nil
}

func teamRunID(team Team) string {
	if team.RunID != "" {
		return team.RunID
	}
	// Compatibility for snapshots registered before run identities were recorded.
	party, mods := matchSide(team.Session, true, 1)
	raw, _ := json.Marshal([]any{party, mods})
	hash := sha256.Sum256(raw)
	return "legacy-" + hex.EncodeToString(hash[:16])
}

func opponentTeamID(id []byte, team Team) string {
	return hex.EncodeToString(id) + ":" + teamRunID(team)
}

func endStreak(st *State, result string) {
	if a := st.StreakAttempt; a != nil && a.Status == "active" {
		a.Status, a.EndedAt = result, time.Now().Unix()
	}
	st.Streak = 0
}

func (s *Service) persistStreak(tx *sql.Tx, id []byte, st *State) error {
	a := st.StreakAttempt
	if a == nil {
		return nil
	}
	raw, err := json.Marshal(a)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT INTO `+s.streaksTable()+`(id,uuid,runId,wins,endedAt,data) VALUES(?,?,?,?,?,?)
		ON DUPLICATE KEY UPDATE wins=VALUES(wins),endedAt=VALUES(endedAt),data=VALUES(data)`, a.ID, id, a.RunID, a.Wins, a.EndedAt, raw)
	return err
}

func (s *Service) newAttempt(tx *sql.Tx, id []byte, st *State, team Team) error {
	runID := teamRunID(team)
	var used int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM `+s.streaksTable()+` WHERE uuid=? AND runId=?`, id, runID).Scan(&used); err != nil {
		return err
	}
	if used > 0 {
		return fmt.Errorf("이 파티의 연승전은 이미 종료되었습니다. 새 클래식 클리어 파티를 등록하세요")
	}
	team.Session.Party = NormalizeParty(team.Session.Party, true)
	team.Session.Modifiers = NormalizeModifiers(team.Session.Modifiers)
	st.StreakAttempt = &StreakAttempt{ID: randomID(), RunID: runID, Status: "active", Wins: st.Streak, StartedAt: time.Now().Unix(), Team: team, Opponents: []string{}}
	return nil
}

// Adopt a still-running pre-upgrade match so its final result also stores a team.
func (s *Service) adoptStreak(tx *sql.Tx, id []byte, st *State) error {
	if st.StreakAttempt != nil || st.Match == nil || st.Match.Practice || st.Match.Status != "active" {
		return nil
	}
	var raw []byte
	if err := tx.QueryRow(`SELECT data FROM `+s.teamsTable()+` WHERE uuid=?`, id).Scan(&raw); err != nil {
		if err == sql.ErrNoRows {
			return nil
		}
		return err
	}
	var team Team
	if err := json.Unmarshal(raw, &team); err != nil {
		return err
	}
	if err := s.newAttempt(tx, id, st, team); err != nil {
		return err
	}
	if st.Match.OpponentTeam != "" {
		st.StreakAttempt.Opponents = append(st.StreakAttempt.Opponents, st.Match.OpponentTeam)
	}
	return nil
}

func (s *Service) startRankedMatch(tx *sql.Tx, id []byte, st *State, selected string) (*Match, error) {
	if len(st.Flags) > 0 {
		return nil, fmt.Errorf("계정 연승전 자격 보류")
	}
	if st.StreakAttempt == nil || st.StreakAttempt.Status != "active" {
		own, err := s.selectedParty(tx, id, selected)
		if err != nil {
			return nil, err
		}
		if err := s.newAttempt(tx, id, st, own); err != nil {
			return nil, err
		}
	}
	a := st.StreakAttempt
	var queries []string
	var args []any
	for _, source := range s.battleProfiles() {
		queries = append(queries, `SELECT t.data,a.username,a.uuid,t.version FROM `+source.teamsTable()+` t JOIN accounts a ON a.uuid=t.uuid
		WHERE t.uuid<>? AND t.version=? AND JSON_VALUE(t.data,'$.auditPolicy')=? AND a.banned=0`)
		args = append(args, id, source.GameVersion(), source.Policy())
		queries = append(queries, `SELECT t.data,a.username,a.uuid,t.version FROM pvpRegisteredParties t JOIN accounts a ON a.uuid=t.uuid
		WHERE t.uuid<>? AND t.version=? AND JSON_VALUE(t.data,'$.auditPolicy')=? AND a.banned=0
		AND NOT EXISTS (SELECT 1 FROM `+source.teamsTable()+` latest WHERE latest.uuid=t.uuid AND latest.version=t.version
		AND COALESCE(JSON_VALUE(latest.data,'$.runId'),'')=COALESCE(JSON_VALUE(t.data,'$.runId'),''))`)
		args = append(args, id, source.GameVersion(), source.Policy())
	}
	rows, err := tx.Query(`SELECT * FROM (`+strings.Join(queries, " UNION ALL ")+`) opponents ORDER BY RAND()`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var name, enemyVersion string
		var enemyID, enemyRaw []byte
		if err = rows.Scan(&enemyRaw, &name, &enemyID, &enemyVersion); err != nil {
			return nil, err
		}
		var enemy Team
		if err = json.Unmarshal(enemyRaw, &enemy); err != nil {
			return nil, err
		}
		if enemy.Session.GameVersion != enemyVersion {
			continue
		}
		key := opponentTeamID(enemyID, enemy)
		if slices.Contains(a.Opponents, key) {
			continue
		}
		a.Opponents = append(a.Opponents, key)
		m := &Match{ID: randomID(), Opponent: name, OpponentTeam: key, Session: SyntheticSession(a.Team.Session, enemy.Session, randomID()[:24]), Decisions: []Decision{}, Status: "active", Created: time.Now().Unix(), Expires: time.Now().Add(30 * time.Minute).Unix()}
		m.OpponentVersion = enemyVersion
		s.bindBattleEngine(m)
		return m, nil
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("아직 만나지 않은 상대 파티가 없습니다. 새 파티가 등록되면 이어 하거나 연승전을 종료해 기록을 등록하세요")
}

func (s *Service) EndStreak(id []byte) error {
	return s.update(id, func(st *State) error {
		if st.StreakAttempt == nil || st.StreakAttempt.Status != "active" {
			return fmt.Errorf("진행 중인 연승전이 없습니다")
		}
		if st.Match != nil && st.Match.Status == "active" && !st.Match.Practice {
			st.Match.Status = "forfeit"
		}
		endStreak(st, "forfeit")
		return nil
	})
}

type LeaderboardEntry struct {
	ID       string             `json:"id"`
	Username string             `json:"username"`
	Wins     int                `json:"wins"`
	Rank     int                `json:"rank"`
	Position int                `json:"position"`
	EndedAt  int64              `json:"endedAt"`
	Result   string             `json:"result"`
	IsSelf   bool               `json:"isSelf"`
	Party    []defs.PokemonData `json:"party"`
}
type Leaderboard struct {
	Version  string             `json:"version"`
	Page     int                `json:"page"`
	PageSize int                `json:"pageSize"`
	Total    int                `json:"total"`
	Entries  []LeaderboardEntry `json:"entries"`
	Self     *LeaderboardEntry  `json:"self"`
}

func (s *Service) Leaderboard(id []byte, page int) (*Leaderboard, error) {
	if page < 0 || page > 1000000 {
		return nil, fmt.Errorf("잘못된 랭킹 페이지")
	}
	// Only finalized attempts are published. Equal win totals share a rank.
	var sources []string
	for _, profile := range s.battleProfiles() {
		sources = append(sources, `SELECT id,uuid,wins,endedAt,data FROM `+profile.streaksTable())
	}
	cte := `WITH ranked AS (SELECT r.id,r.uuid,a.username,r.wins,r.endedAt,r.data,
		RANK() OVER (ORDER BY r.wins DESC) AS place,
		ROW_NUMBER() OVER (ORDER BY r.wins DESC,r.endedAt,r.id) AS position FROM (` + strings.Join(sources, ` UNION ALL `) + `) r JOIN accounts a ON a.uuid=r.uuid WHERE r.endedAt>0 AND a.banned=0) `
	out := &Leaderboard{Version: s.GameVersion(), Page: page, PageSize: 5, Entries: []LeaderboardEntry{}}
	tx, err := s.DB.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err = tx.QueryRow(cte + `SELECT COUNT(*) FROM ranked`).Scan(&out.Total); err != nil {
		return nil, err
	}
	if out.Total > 0 {
		out.Page = min(page, (out.Total-1)/out.PageSize)
	} else {
		out.Page = 0
	}
	read := func(rows *sql.Rows) ([]LeaderboardEntry, error) {
		defer rows.Close()
		entries := []LeaderboardEntry{}
		for rows.Next() {
			var e LeaderboardEntry
			var raw []byte
			if err := rows.Scan(&e.ID, &e.Username, &e.Wins, &e.EndedAt, &e.Rank, &e.Position, &e.IsSelf, &raw); err != nil {
				return nil, err
			}
			var a StreakAttempt
			if err := json.Unmarshal(raw, &a); err != nil {
				return nil, err
			}
			e.Result = a.Status
			for _, p := range a.Team.Session.Party {
				m := asObject(p)
				summary := Object{}
				for _, key := range []string{"species", "formIndex", "fusionSpecies", "fusionFormIndex", "level", "shiny", "variant"} {
					if v, ok := m[key]; ok {
						summary[key] = v
					}
				}
				e.Party = append(e.Party, summary)
			}
			entries = append(entries, e)
		}
		return entries, rows.Err()
	}
	columns := `SELECT id,username,wins,endedAt,place,position,uuid=?,data FROM ranked `
	rows, err := tx.Query(cte+columns+`ORDER BY wins DESC,endedAt,id LIMIT ? OFFSET ?`, id, out.PageSize, out.Page*out.PageSize)
	if err != nil {
		return nil, err
	}
	if out.Entries, err = read(rows); err != nil {
		return nil, err
	}
	rows, err = tx.Query(cte+columns+`WHERE uuid=? ORDER BY wins DESC,endedAt,id LIMIT 1`, id, id)
	if err != nil {
		return nil, err
	}
	self, err := read(rows)
	if err != nil {
		return nil, err
	}
	if len(self) > 0 {
		out.Self = &self[0]
	}
	return out, tx.Commit()
}

func (s *Service) RankedTeam(entryID string) (*Team, error) {
	var raw []byte
	for _, profile := range s.battleProfiles() {
		err := s.DB.QueryRow(`SELECT r.data FROM `+profile.streaksTable()+` r JOIN accounts a ON a.uuid=r.uuid WHERE r.id=? AND r.endedAt>0 AND a.banned=0`, entryID).Scan(&raw)
		if err == nil {
			break
		}
		if err != sql.ErrNoRows {
			return nil, err
		}
	}
	if raw == nil {
		return nil, fmt.Errorf("종료된 연승전 기록이 없습니다")
	}
	var a StreakAttempt
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, err
	}
	// Public party details exclude the run's trainer identity and campaign state.
	return &Team{Session: defs.SessionSaveData{Party: a.Team.Session.Party, Modifiers: a.Team.Session.Modifiers}}, nil
}

// RegisteredTeam exposes only the authenticated player's immutable audited party.
func (s *Service) RegisteredTeam(id []byte) (*Team, error) {
	var raw []byte
	if err := s.DB.QueryRow(`SELECT data FROM `+s.teamsTable()+` WHERE uuid=? AND version=? AND JSON_VALUE(data,'$.auditPolicy')=?`, id, s.GameVersion(), s.Policy()).Scan(&raw); err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("등록된 파티가 없습니다")
		}
		return nil, err
	}
	var team Team
	if err := json.Unmarshal(raw, &team); err != nil {
		return nil, err
	}
	return &Team{Session: defs.SessionSaveData{Party: team.Session.Party, Modifiers: team.Session.Modifiers}}, nil
}
