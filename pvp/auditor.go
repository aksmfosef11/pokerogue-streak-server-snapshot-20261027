// SPDX-License-Identifier: AGPL-3.0-or-later
package pvp

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"
)

type AuditJob struct {
	Kind     string        `json:"kind,omitempty"`
	EggJob   *EggAuditJob  `json:"eggJob,omitempty"`
	Run      *Run          `json:"run"`
	Logs     []Log         `json:"logs"`
	Manifest RulesManifest `json:"manifest"`
}
type VerifiedDreamMove struct {
	Species   int `json:"species"`
	MoveIndex int `json:"moveIndex"`
}
type AuditResult struct {
	Policy            string              `json:"policy"`
	PartyVerified     bool                `json:"partyVerified"`
	InventoryVerified bool                `json:"inventoryVerified"`
	FormItemsVerified bool                `json:"formItemsVerified"`
	LevelsVerified    bool                `json:"levelsVerified"`
	MoneyVerified     bool                `json:"moneyVerified"`
	Eggs              []VerifiedEgg       `json:"eggs,omitempty"`
	DreamEggMoves     []VerifiedDreamMove `json:"dreamEggMoves,omitempty"`
	OK                bool                `json:"ok"`
	Reason            string              `json:"reason"`
	ElapsedMS         int64               `json:"elapsedMs"`
	MaxRSS            int64               `json:"maxRSS"`
}

func (s *Service) enqueueAudit(id []byte) {
	s.auditMu.Lock()
	defer s.auditMu.Unlock()
	key := hex.EncodeToString(id)
	if s.auditActive[key] {
		return
	}
	if s.registry != nil {
		select {
		case s.registry.queueSlots <- struct{}{}:
		default:
			return
		}
	}
	select {
	case s.queue <- append([]byte(nil), id...):
		s.auditActive[key] = true
	default: // Durable pending jobs are picked up by the recovery scan.
		if s.registry != nil {
			<-s.registry.queueSlots
		}
	}
}
func (s *Service) recoverAudits() {
	if err := s.finishExpiredStreaks(); err != nil {
		log.Printf("pvp streak recovery: %v", err)
	}
	rows, err := s.DB.QueryContext(s.ctx, `SELECT uuid FROM `+s.accountsTable()+` WHERE (JSON_VALUE(data,'$.audit.status')='pending' AND COALESCE(JSON_VALUE(data,'$.audit.nextAttempt'),0)<=?) OR (COALESCE(JSON_LENGTH(JSON_EXTRACT(data,'$.eggJobs')),0)>0 AND COALESCE(JSON_VALUE(data,'$.eggJobs[0].nextAttempt'),0)<=?)`, time.Now().Unix(), time.Now().Unix())
	if err != nil {
		log.Printf("pvp audit recovery: %v", err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var id []byte
		if rows.Scan(&id) == nil {
			s.enqueueAudit(id)
		}
	}
}
func (s *Service) startAuditWorkers() {
	n, _ := strconv.Atoi(os.Getenv("PVP_AUDIT_WORKERS"))
	n = max(1, min(n, 4))
	s.workers.Add(n + 1)
	for i := 0; i < n; i++ {
		go func() {
			defer s.workers.Done()
			for {
				select {
				case <-s.ctx.Done():
					return
				case id := <-s.queue:
					if err := s.auditOne(id); err != nil {
						log.Printf("pvp audit: %v", err)
					}
					s.auditMu.Lock()
					delete(s.auditActive, hex.EncodeToString(id))
					s.auditMu.Unlock()
					if s.registry != nil {
						<-s.registry.queueSlots
					}
				}
			}
		}()
	}
	go func() {
		defer s.workers.Done()
		s.recoverAudits()
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-s.ctx.Done():
				return
			case <-ticker.C:
				s.recoverAudits()
			}
		}
	}()
}
func (s *Service) verifyGeneration(job AuditJob) (AuditResult, error) {
	var result AuditResult
	if s.auditURL == "" {
		return result, fmt.Errorf("생성 감사 워커가 설정되지 않았습니다")
	}
	body, err := json.Marshal(job)
	if err != nil {
		return result, err
	}
	ctx := s.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	if s.registry != nil {
		select {
		case s.registry.auditSlots <- struct{}{}:
			defer func() { <-s.registry.auditSlots }()
		case <-ctx.Done():
			return result, ctx.Err()
		}
	}
	req, err := http.NewRequestWithContext(ctx, "POST", s.auditURL+"/verify", bytes.NewReader(body))
	if err != nil {
		return result, err
	}
	h := hmac.New(sha256.New, s.Secret)
	h.Write([]byte("pvp-audit-v1"))
	req.Header.Set("Authorization", "Bearer "+hex.EncodeToString(h.Sum(nil)))
	req.Header.Set("Content-Type", "application/json")
	response, err := (&http.Client{Timeout: 310 * time.Second}).Do(req)
	if err != nil {
		return result, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return result, fmt.Errorf("생성 감사 워커 응답: %d", response.StatusCode)
	}
	err = json.NewDecoder(io.LimitReader(response.Body, 16384)).Decode(&result)
	if err == nil && job.Kind != "eggs" && (result.Policy != s.Policy() || (result.OK && (!result.PartyVerified || !result.InventoryVerified || !result.FormItemsVerified || !result.LevelsVerified || !result.MoneyVerified))) {
		result.OK = false
		result.Reason = "감사 정책/파티·아이템·폼·레벨·돈 검증이 없는 워커 결과"
		return result, fmt.Errorf("감사 워커 갱신 필요: %s", result.Reason)
	}
	return result, err
}
func (s *Service) auditOne(id []byte) error {
	if err := s.auditEggOne(id); err != nil {
		return err
	}
	var job AuditJob
	var auditID string
	err := s.update(id, func(st *State) error {
		a := st.Audit
		if a == nil || a.Status != "pending" || a.NextAttempt > time.Now().Unix() {
			return nil
		}
		r := st.Runs[a.RunID]
		var failure error
		if r == nil {
			failure = fmt.Errorf("런 기록 없음")
		} else if len(st.Flags) > 0 {
			failure = fmt.Errorf("계정 자격 보류")
		} else {
			failure = ValidateLogs(r, a.Logs)
		}
		if failure != nil {
			a.Status = "rejected"
			a.Reason = failure.Error()
			a.Logs = nil
			return nil
		}
		job = AuditJob{Run: r, Logs: a.Logs, Manifest: s.Manifest}
		a.Attempts++
		a.NextAttempt = time.Now().Add(auditRetryDelay(a.Attempts)).Unix()
		auditID = a.ID
		return nil
	})
	if err != nil || job.Run == nil {
		return err
	}
	result, err := s.verifyGeneration(job) // Never hold an account row lock while the engine runs.
	if err != nil {
		_ = s.update(id, func(st *State) error {
			if a := st.Audit; a != nil && a.ID == auditID && a.Status == "pending" {
				a.Reason = "감사 워커 대기/재시도"
			}
			return nil
		})
		return err
	}
	// Publish the approved team and status together, rechecking qualification under
	// the same row lock used by saves, decisions and registrations.
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var raw []byte
	if err = tx.QueryRow(`SELECT data FROM `+s.accountsTable()+` WHERE uuid=? FOR UPDATE`, id).Scan(&raw); err != nil {
		return err
	}
	var st State
	if err = json.Unmarshal(raw, &st); err != nil {
		return err
	}
	a := st.Audit
	if a == nil || a.ID != auditID || a.Status != "pending" {
		return nil
	}
	a.ElapsedMS, a.MaxRSS = result.ElapsedMS, result.MaxRSS
	a.NextAttempt = 0
	a.Status, a.Reason, a.Logs = "rejected", result.Reason, nil
	if len(st.Flags) > 0 {
		a.Reason = "계정 자격 보류"
	} else if result.OK {
		initEggTrust(&st)
		for _, dm := range result.DreamEggMoves {
			if dm.Species > 0 && dm.MoveIndex >= 0 && dm.MoveIndex < 4 {
				st.VerifiedEggMoves[dm.Species] |= 1 << dm.MoveIndex
				t := st.TrustedStarters[dm.Species]
				t.EggMoves = st.VerifiedEggMoves[dm.Species]
				st.TrustedStarters[dm.Species] = t
			}
		}
		team := &Team{AuditPolicy: s.Policy(), RunID: job.Run.ID, Session: *job.Run.Candidate}
		team.Session.Party = NormalizeParty(team.Session.Party, true)
		team.Session.Modifiers = NormalizeModifiers(team.Session.Modifiers)
		data, e := json.Marshal(team)
		if e != nil {
			return e
		}
		// Keep the old audited party before publishing the new one. Legacy
		// clients still see the latest party; the archive keeps all choices.
		var previous []byte
		if e = tx.QueryRow(`SELECT data FROM `+s.teamsTable()+` WHERE uuid=? AND version=?`, id, job.Run.Version).Scan(&previous); e != nil && e != sql.ErrNoRows {
			return e
		}
		if e == nil {
			var old Team
			if e = json.Unmarshal(previous, &old); e != nil {
				return e
			}
			if _, e = tx.Exec(`INSERT IGNORE INTO pvpRegisteredParties(uuid,version,runId,data,registeredAt) VALUES(?,?,?,?,?)`, id, job.Run.Version, teamRunID(old), previous, time.Now().Unix()); e != nil {
				return e
			}
		}
		if _, err = tx.Exec(`INSERT INTO pvpRegisteredParties(uuid,version,runId,data,registeredAt) VALUES(?,?,?,?,?) ON DUPLICATE KEY UPDATE data=VALUES(data)`, id, job.Run.Version, teamRunID(*team), data, time.Now().Unix()); err != nil {
			return err
		}
		if _, err = tx.Exec(`REPLACE INTO `+s.teamsTable()+`(uuid,version,data) VALUES(?,?,?)`, id, job.Run.Version, data); err != nil {
			return err
		}
		a.Status, a.Reason = "approved", ""
		if st.StreakAttempt == nil || st.StreakAttempt.Status != "active" {
			st.Streak, st.StreakAttempt = 0, nil
		}
	}
	raw, err = json.Marshal(&st)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(`UPDATE `+s.accountsTable()+` SET data=? WHERE uuid=?`, raw, id); err != nil {
		return err
	}
	return tx.Commit()
}

func auditRetryDelay(attempt int) time.Duration {
	return time.Duration(min(30*(1<<min(max(attempt-1, 0), 5)), 600)) * time.Second
}
