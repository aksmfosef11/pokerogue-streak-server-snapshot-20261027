// SPDX-License-Identifier: AGPL-3.0-or-later
package pvp

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/pagefaultgames/rogueserver/defs"
	"sync"
	"time"
)

type Service struct {
	DB          *sql.DB
	Secret      []byte
	Rules       Rules
	Manifest    RulesManifest
	profile     *VersionProfile
	registry    *Registry
	Practice    bool
	queue       chan []byte
	auditURL    string
	auditMu     sync.Mutex
	auditActive map[string]bool
	ctx         context.Context
	cancel      context.CancelFunc
	workers     sync.WaitGroup
}

func New(database *sql.DB) (*Service, error) {
	r, err := NewRegistry(database)
	if err != nil {
		return nil, err
	}
	return r.Default(), nil
}

// Close stops background audit work without discarding durable pending jobs.
// The database is shared with the ordinary save API and remains open.
func (s *Service) Close() {
	if s.registry != nil {
		s.registry.Close()
		return
	}
	if s.cancel != nil {
		s.cancel()
		s.workers.Wait()
	}
}
func randomID() string {
	b := make([]byte, 16)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b)
}
func (s *Service) update(id []byte, f func(*State) error) error {
	return s.updateTransaction(id, func(st *State, _ *sql.Tx) error { return f(st) })
}
func (s *Service) updateTransaction(id []byte, f func(*State, *sql.Tx) error) error {
	// Finish INSERT IGNORE in its own transaction before taking the exclusive
	// row lock. Keeping its shared duplicate-key lock caused lock-upgrade
	// deadlocks when two tabs submitted the same decision concurrently.
	if _, e := s.DB.Exec(`INSERT IGNORE INTO `+s.accountsTable()+`(uuid,data) VALUES(?,'{}')`, id); e != nil {
		return e
	}
	tx, e := s.DB.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var raw []byte
	if e = tx.QueryRow(`SELECT data FROM `+s.accountsTable()+` WHERE uuid=? FOR UPDATE`, id).Scan(&raw); e != nil {
		return e
	}
	st := &State{}
	if e = json.Unmarshal(raw, st); e != nil {
		return e
	}
	if st.Runs == nil {
		st.Runs = map[string]*Run{}
	}
	if st.Observed == nil {
		st.Observed = map[string]int{}
	}
	if e = s.adoptStreak(tx, id, st); e != nil {
		return e
	}
	expire(st)
	if e = f(st, tx); e != nil {
		return e
	}
	if e = s.persistStreak(tx, id, st); e != nil {
		return e
	}
	raw, e = json.Marshal(st)
	if e != nil {
		return e
	}
	if _, e = tx.Exec(`UPDATE `+s.accountsTable()+` SET data=? WHERE uuid=?`, raw, id); e != nil {
		return e
	}
	return tx.Commit()
}
func expire(st *State) {
	if st.Match != nil && st.Match.Status == "active" && st.Match.Expires < time.Now().Unix() {
		st.Match.Status = "timeout"
		if st.Match.Practice {
			st.PracticeStreak = 0
		} else {
			endStreak(st, "timeout")
		}
	}
}
func (s *Service) Status(id []byte) (Object, error) {
	var out Object
	e := s.update(id, func(st *State) error {
		expire(st)
		out = Object{"flags": st.Flags, "eggStatistics": st.EggStatistics, "audit": st.Audit, "match": st.Match, "streak": st.Streak, "best": st.Best, "practiceStreak": st.PracticeStreak, "practiceBest": st.PracticeBest, "practiceEnabled": s.Practice, "runs": st.Runs}
		if a := st.StreakAttempt; a != nil {
			out["streakAttempt"] = Object{"id": a.ID, "runId": a.RunID, "status": a.Status, "wins": a.Wins, "endedAt": a.EndedAt, "opponents": len(a.Opponents)}
		}
		out["eggVerification"] = Object{"pendingJobs": len(st.EggJobs), "certificates": len(st.EggCertificates), "reason": st.EggAuditError, "usedThrough": st.EggUsed}
		out["battleVersion"] = s.BattleVersion()
		out["sourceVersion"] = s.GameVersion()
		paths := map[string]string{}
		for _, profile := range s.battleProfiles() {
			paths[profile.GameVersion()] = profile.ClientPath()
		}
		out["enginePaths"] = paths
		if s.registry != nil {
			if target, ok := s.registry.Lookup(s.BattleVersion()); ok {
				out["battlePath"] = target.ClientPath()
			}
			if st.Match != nil {
				match := *st.Match
				match.SourceVersion = s.GameVersion()
				if engine, ok := match.Session["gameVersion"].(string); ok {
					if profile, ok := s.registry.Lookup(engine); ok {
						match.BattlePath = profile.ClientPath()
					}
				}
				out["match"] = &match
			}
		}
		if s.registry != nil && s.BattleVersion() != s.GameVersion() {
			if engine, ok := s.registry.Lookup(s.BattleVersion()); ok {
				out["newGamePath"] = engine.profile.ClientPath
			}
		}
		if st.Audit != nil {
			summary := *st.Audit
			summary.Logs = nil
			out["audit"] = &summary
		}
		return nil
	})
	if e != nil {
		return nil, e
	}
	var n int
	e = s.DB.QueryRow(`SELECT COUNT(*) FROM `+s.teamsTable()+` WHERE uuid=? AND version=? AND JSON_VALUE(data,'$.auditPolicy')=?`, id, s.GameVersion(), s.Policy()).Scan(&n)
	if e == nil && n > 0 {
		out["defense"], e = s.RegisteredDefense(id)
	}
	if e == nil && n == 0 {
		e = s.DB.QueryRow(`SELECT COUNT(*) FROM pvpRegisteredParties WHERE uuid=? AND version=? AND JSON_VALUE(data,'$.auditPolicy')=?`, id, s.GameVersion(), s.Policy()).Scan(&n)
	}
	out["registered"] = n > 0
	return out, e
}
func (s *Service) StartRun(id []byte, version string, starters []Starter, system defs.SystemSaveData) (*Run, error) {
	if version != s.GameVersion() || system.GameVersion != version {
		return nil, fmt.Errorf("지원하지 않는 게임 버전입니다")
	}
	if len(starters) == 0 || len(starters) > 6 {
		return nil, fmt.Errorf("스타터 파티가 올바르지 않습니다")
	}
	var run *Run
	e := s.update(id, func(st *State) error {
		if !st.Baseline {
			return fmt.Errorf("시스템 저장을 먼저 동기화하세요")
		}
		if len(st.Flags) > 0 {
			return fmt.Errorf("계정 연승전 자격이 보류되었습니다: %v", st.Flags)
		}
		if st.TrustSchema != 1 || st.SystemHashSchema != 2 || st.SystemHash == "" || st.SystemHash != systemFingerprint(system) {
			return fmt.Errorf("검증된 시스템 저장과 일치하지 않습니다. 먼저 시스템 저장을 동기화하세요")
		}
		budget := 0.0
		seen := map[int]bool{}
		for _, p := range starters {
			r, ok := s.Rules[p.Species]
			if !ok || r.Cost < 1 || seen[p.Species] {
				return fmt.Errorf("유효하지 않은 스타터")
			}
			seen[p.Species] = true
			entry := st.TrustedStarters[p.Species]
			if p.Passive && entry.PassiveAttr&1 == 0 {
				return fmt.Errorf("미해금 패시브")
			}
			cost := float64(r.Cost)
			for i := 0; i < entry.ValueReduction; i++ {
				if cost <= 1 {
					cost /= 2
				} else {
					cost--
				}
			}
			budget += cost
			if err := validateStarterMoves(p, r, entry); err != nil {
				return err
			}
		}
		if budget > 10 {
			return fmt.Errorf("스타터 코스트 예산 초과")
		}
		if len(st.Runs) >= 10 {
			var oldest string
			var ts int64 = 1 << 62
			for k, r := range st.Runs {
				if r.Created < ts {
					oldest = k
					ts = r.Created
				}
			}
			delete(st.Runs, oldest)
		}
		run = &Run{ID: randomID(), Seed: randomID()[:24], Version: version, Created: time.Now().Unix(), Starters: starters, Anchors: []Anchor{}}
		contract := s.RunContract()
		run.Contract = &contract
		run.Identity = Object{"trainerId": system.TrainerId, "secretId": system.SecretId}
		run.TrustEpoch = st.TrustEpoch
		st.Runs[run.ID] = run
		return nil
	})
	return run, e
}
func (s *Service) Anchor(id []byte, runID string, wave int, head string) error {
	return s.update(id, func(st *State) error {
		r := st.Runs[runID]
		if r == nil {
			return fmt.Errorf("런을 찾을 수 없습니다")
		}
		if wave < 1 || wave > 200 || len(head) != 64 {
			return fmt.Errorf("잘못된 앵커")
		}
		if _, e := hex.DecodeString(head); e != nil {
			return e
		}
		for _, a := range r.Anchors {
			if a.Wave == wave {
				if a.Head != head {
					return fmt.Errorf("동일 웨이브의 앵커는 변경할 수 없습니다")
				}
				return nil
			}
		}
		if len(r.Anchors) > 0 && r.Anchors[len(r.Anchors)-1].Wave > wave {
			return fmt.Errorf("앵커 순서 오류")
		}
		r.Anchors = append(r.Anchors, Anchor{Wave: wave, Head: head, At: time.Now().Unix()})
		return nil
	})
}
func (s *Service) ObserveClear(id []byte, session defs.SessionSaveData) error {
	return s.update(id, func(st *State) error {
		if session.GameMode != 0 || session.WaveIndex != 200 || session.BattleType != 2 {
			return nil
		}
		for _, r := range st.Runs {
			if r.Seed == session.Seed && r.Version == session.GameVersion && r.Candidate == nil {
				copy := session
				r.Candidate = &copy
				r.ClearedAt = time.Now().Unix()
			}
		}
		return nil
	})
}
func (s *Service) Register(id []byte, runID string, logs []Log) error {
	if len(logs) != 200 {
		return fmt.Errorf("1~200웨이브의 로컬 기록이 필요합니다")
	}
	legacyHistory, err := legacyRegistrationHistory(s.DB, id)
	if err != nil {
		return err
	}
	if _, err := s.DB.Exec(`INSERT IGNORE INTO pvpRegistrationLimits(uuid,data) VALUES(?,'{}')`, id); err != nil {
		return err
	}
	e := s.updateTransaction(id, func(st *State, tx *sql.Tx) error {
		if len(st.Flags) > 0 {
			return fmt.Errorf("연승전 자격 보류: %v", st.Flags)
		}
		r := st.Runs[runID]
		if r == nil || r.Candidate == nil {
			return fmt.Errorf("서버에서 확인된 클래식 클리어가 없습니다")
		}
		if r.TrustEpoch != st.TrustEpoch {
			return fmt.Errorf("기준선 재설정 후 시작한 클래식 런이 필요합니다")
		}
		if st.Audit != nil && st.Audit.RunID == runID && (st.Audit.Status == "pending" || st.Audit.Status == "approved") {
			return nil // Retrying after a lost response must not create another audit.
		}
		if st.Audit != nil && st.Audit.Status == "pending" {
			return fmt.Errorf("감사가 이미 진행 중입니다")
		}
		var used int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM `+s.streaksTable()+` WHERE uuid=? AND runId=?`, id, runID).Scan(&used); err != nil {
			return err
		}
		if used > 0 {
			return fmt.Errorf("이미 연승전에 사용한 클리어 파티입니다. 새 클래식 런이 필요합니다")
		}
		now := time.Now().Unix()
		recent := recentRegistrations(st.Registrations, now)
		histories := map[string][]int64{legacyStorageVersion: legacyHistory}
		histories[s.GameVersion()] = recent
		if err := reserveRegistration(tx, id, s.GameVersion(), now, histories); err != nil {
			return err
		}
		st.Registrations = append(recent, now)
		st.Audit = &Audit{ID: randomID(), RunID: runID, Status: "pending", Logs: logs}
		return nil
	})
	if e != nil {
		return e
	}
	s.enqueueAudit(id)
	return nil
}
func (s *Service) StartMatch(id []byte, practice bool, teamID ...string) (*Match, error) {
	if practice && !s.Practice {
		return nil, fmt.Errorf("연습전이 비활성화되었습니다")
	}
	var match *Match
	var terminalError error
	err := s.updateTransaction(id, func(st *State, tx *sql.Tx) error {
		expire(st)
		if st.Match != nil && st.Match.Status == "active" {
			match = st.Match
			return nil
		}
		if practice {
			team := PracticeTeam()
			match = &Match{ID: randomID(), Practice: true, Opponent: "샘플 트레이너", Session: SyntheticSession(team.Session, team.Session, randomID()[:24]), Decisions: []Decision{}, Status: "active", Created: time.Now().Unix(), Expires: time.Now().Add(30 * time.Minute).Unix()}
			s.bindBattleEngine(match)
		} else {
			var err error
			selected := ""
			if len(teamID) > 0 {
				selected = teamID[0]
			}
			match, err = s.startRankedMatch(tx, id, st, selected)
			if err != nil {
				if st.StreakAttempt != nil && st.StreakAttempt.Status != "active" {
					terminalError = err
					return nil
				}
				return err
			}
		}
		st.Match = match
		return nil
	})
	if err == nil {
		err = terminalError
	}
	return match, err
}
func (s *Service) decisionSeed(matchID string, index int) string {
	h := hmac.New(sha256.New, s.Secret)
	if s.GameVersion() != legacyStorageVersion {
		fmt.Fprintf(h, "pvp-version-v1:%s:", s.GameVersion())
	}
	fmt.Fprintf(h, "%s:%d", matchID, index)
	return hex.EncodeToString(h.Sum(nil))
}
func LockDecision(m *Match, d Decision, seed string) (Decision, error) {
	if d.Index < 0 || d.Index > 512 || len(d.Digest) != 64 {
		return d, fmt.Errorf("잘못된 결정")
	}
	if _, e := hex.DecodeString(d.Digest); e != nil {
		return d, e
	}
	if d.Index < len(m.Decisions) {
		old := m.Decisions[d.Index]
		if old.Digest != d.Digest || old.Kind != d.Kind {
			return d, fmt.Errorf("재생 상태가 일치하지 않습니다")
		}
		return old, nil
	}
	if d.Index != len(m.Decisions) {
		return d, fmt.Errorf("결정 순서 오류")
	}
	if d.Kind != "turn" && d.Kind != "switch" {
		return d, fmt.Errorf("지원하지 않는 결정")
	}
	if len(d.Choice) == 0 || len(d.Choice) > 8192 || !json.Valid(d.Choice) {
		return d, fmt.Errorf("잘못된 선택")
	}
	d.Seed = seed
	m.Decisions = append(m.Decisions, d)
	return d, nil
}
func (s *Service) Commit(id []byte, matchID string, d Decision, engine ...string) (Decision, error) {
	var out Decision
	var conflict error
	e := s.update(id, func(st *State) error {
		expire(st)
		m := st.Match
		if len(engine) > 0 && m != nil && m.Session["gameVersion"] != engine[0] {
			return fmt.Errorf("전투 버전이 일치하지 않습니다")
		}
		if m == nil || m.ID != matchID || m.Status != "active" {
			return fmt.Errorf("진행 중인 매치가 없습니다")
		}
		var err error
		out, err = lockDecisionAt(m, d, s.decisionSeed(m.ID, d.Index), time.Now().Unix())
		if err != nil {
			if d.Index < len(m.Decisions) {
				invalidateMatch(st)
				conflict = err
				return nil
			}
			return err
		}
		return nil
	})
	if e == nil {
		e = conflict
	}
	return out, e
}
func resultPartyAlive(hp []int) (bool, error) {
	alive := false
	for _, value := range hp {
		if value < 0 || value > MaxResultHP {
			return false, fmt.Errorf("잘못된 HP")
		}
		alive = alive || value > 0
	}
	return alive, nil
}
func (s *Service) Finish(id []byte, matchID, result string, playerHP, enemyHP []int, index int, engine ...string) (*Match, error) {
	var out *Match
	e := s.updateTransaction(id, func(st *State, tx *sql.Tx) error {
		expire(st)
		m := st.Match
		if m == nil || m.ID != matchID {
			return fmt.Errorf("매치를 찾을 수 없습니다")
		}
		out = m
		if m.Status != "active" {
			return nil
		}
		if result != "forfeit" && len(engine) > 0 && m.Session["gameVersion"] != engine[0] {
			return fmt.Errorf("전투 버전이 일치하지 않습니다")
		}
		if result != "invalid" && result != "forfeit" {
			if index != len(m.Decisions) || index == 0 || len(playerHP) != len(m.Session["party"].([]any)) || len(enemyHP) != len(m.Session["enemyParty"].([]any)) {
				return fmt.Errorf("결과 상태 오류")
			}
			aliveP, err := resultPartyAlive(playerHP)
			if err != nil {
				return err
			}
			aliveE, err := resultPartyAlive(enemyHP)
			if err != nil {
				return err
			}
			if (result == "won" && (!aliveP || aliveE)) || (result == "lost" && aliveP) || (result != "won" && result != "lost") {
				return fmt.Errorf("결과가 파티 상태와 다릅니다")
			}
		}
		if result == "invalid" {
			invalidateMatch(st)
			return nil
		}
		if !m.Practice && (result == "won" || result == "lost") {
			if err := recordDefense(tx, m, s.GameVersion(), result); err != nil {
				return err
			}
		}
		m.Status = result
		if m.Practice {
			if result == "won" {
				st.PracticeStreak++
			} else {
				st.PracticeStreak = 0
			}
			if st.PracticeStreak > st.PracticeBest {
				st.PracticeBest = st.PracticeStreak
			}
		} else {
			if result == "won" {
				st.Streak++
				if st.StreakAttempt != nil {
					st.StreakAttempt.Wins = st.Streak
				}
			} else {
				endStreak(st, result)
			}
			if st.Streak > st.Best {
				st.Best = st.Streak
			}
		}
		return nil
	})
	return out, e
}
