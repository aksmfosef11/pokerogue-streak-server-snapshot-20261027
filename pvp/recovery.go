// SPDX-License-Identifier: AGPL-3.0-or-later
package pvp

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"github.com/pagefaultgames/rogueserver/defs"
	"sort"
	"strings"
	"time"
)

// ReviewBaseline and ApproveBaseline are local operator interfaces. They are
// deliberately absent from the public HTTP API and never modify ordinary saves.
func (s *Service) ReviewBaseline(id []byte, system defs.SystemSaveData) (Object, error) {
	var raw []byte
	state := State{}
	err := s.DB.QueryRow(`SELECT data FROM `+s.accountsTable()+` WHERE uuid=?`, id).Scan(&raw)
	if err != nil && err != sql.ErrNoRows {
		return nil, err
	}
	if err == nil {
		if err = json.Unmarshal(raw, &state); err != nil {
			return nil, err
		}
	}
	ids := []int{}
	for species, entry := range system.StarterData {
		if entry.CandyCount != 0 || entry.ValueReduction != 0 || entry.PassiveAttr != 0 || entry.EggMoves != 0 {
			ids = append(ids, species)
		}
	}
	sort.Ints(ids)
	entries := []Object{}
	for _, species := range ids {
		p := system.StarterData[species]
		entries = append(entries, Object{"species": species, "candies": p.CandyCount, "reduction": p.ValueReduction, "passive": p.PassiveAttr, "eggMoves": p.EggMoves, "trusted": state.TrustedStarters[species]})
	}
	return Object{"gameVersion": s.GameVersion(), "baseline": state.Baseline, "flags": state.Flags, "eggStatistics": state.EggStatistics, "epoch": state.TrustEpoch, "systemMatches": state.SystemHashSchema == 2 && state.SystemHash == systemFingerprint(system), "starters": entries, "reviews": state.Reviews, "activeMatch": state.Match != nil && state.Match.Status == "active" && state.Match.Expires >= time.Now().Unix()}, nil
}
func (s *Service) ApproveBaseline(id []byte, system defs.SystemSaveData, operator, reason string) error {
	operator = strings.TrimSpace(operator)
	reason = strings.TrimSpace(reason)
	if operator == "" || len(operator) > 80 || reason == "" || len(reason) > 500 {
		return fmt.Errorf("operator and review reason are required (max 80/500 bytes)")
	}
	if len(s.Rules) == 0 {
		return fmt.Errorf("exported rules are required for baseline review")
	}
	if system.GameVersion != s.GameVersion() || (system.TrainerId == 0 && system.SecretId == 0) {
		return fmt.Errorf("sync a current-version system save before baseline approval")
	}
	if flags := ValidateSystem(system, system, s.Rules, 0); len(flags) > 0 {
		return fmt.Errorf("baseline contains invalid values: %v", flags)
	}
	for _, p := range system.DexData {
		if p.CaughtCount < 0 || p.HatchedCount < 0 {
			return fmt.Errorf("negative baseline counters")
		}
	}
	return s.updateTransaction(id, func(state *State, tx *sql.Tx) error {
		expire(state)
		if state.Match != nil && state.Match.Status == "active" {
			return fmt.Errorf("finish the active match before resetting its qualification baseline")
		}
		hash := systemFingerprint(system)
		if state.StreakAttempt != nil && state.StreakAttempt.Status == "active" {
			return fmt.Errorf("finish the active streak before resetting its qualification baseline")
		}
		state.TrustEpoch++
		state.Reviews = append(state.Reviews, BaselineReview{At: time.Now().Unix(), Operator: operator, Reason: reason, PreviousFlags: append([]string(nil), state.Flags...), PreviousHash: state.SystemHash, NewHash: hash, Epoch: state.TrustEpoch})
		if len(state.Reviews) > 32 {
			state.Reviews = state.Reviews[len(state.Reviews)-32:]
		}
		state.Flags = nil
		state.Baseline = true
		state.SystemHash = hash
		state.SystemHashSchema = 2
		state.EggTrustSchema = 1
		state.VerifiedEggMoves = map[int]int{}
		for id, p := range system.StarterData {
			state.VerifiedEggMoves[id] = p.EggMoves
		}
		state.CurrentEggMoves = state.VerifiedEggMoves
		state.EggTickets = nil
		state.EggUsed = state.EggIssued
		state.EggJobs = nil
		state.EggHatches = nil
		state.EggCertificates = nil
		state.EggRecent = nil
		trustStarters(state, system)
		state.CandyCarry = nil
		state.Income = nil
		state.EggStatistics = EggStatistics{}
		state.WaveCredit = 0
		state.LastWaveAt = time.Now().Unix()
		state.Streak = 0
		if state.Audit != nil && state.Audit.Status == "pending" {
			state.Audit.Status = "cancelled"
			state.Audit.Reason = "operator baseline reset"
			state.Audit.Logs = nil
		}
		// Retain the old snapshot for inspection, but retire its certification. It
		// cannot enter the current match pool until a fresh run passes registration.
		_, err := tx.Exec(`UPDATE `+s.teamsTable()+` SET data=JSON_SET(data,'$.auditPolicy','retired','$.retiredAt',?) WHERE uuid=?`, time.Now().Unix(), id)
		if err != nil {
			return err
		}
		_, err = tx.Exec(`UPDATE pvpRegisteredParties SET data=JSON_SET(data,'$.auditPolicy','retired','$.retiredAt',?) WHERE uuid=? AND version=?`, time.Now().Unix(), id, s.GameVersion())
		return err
	})
}
