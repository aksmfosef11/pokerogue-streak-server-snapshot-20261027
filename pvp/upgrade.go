// SPDX-License-Identifier: AGPL-3.0-or-later
package pvp

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"github.com/pagefaultgames/rogueserver/defs"
	"math/big"
)

// Mirror only the pinned v1.12.2.0 save migration, never a new random egg roll.
func (s *Service) eggBasis(system defs.SystemSaveData) defs.SystemSaveData {
	if s.GameVersion() != "1.12.2.0" || system.GameVersion == s.GameVersion() {
		return system
	}
	sourceRules := s.Rules
	if s.registry != nil {
		if source, ok := s.registry.Lookup(system.GameVersion); ok {
			sourceRules = source.Rules
		}
	}
	copy := system
	copy.StarterData = defs.StarterData{}
	for id, entry := range system.StarterData {
		entry.Pity = []int{0, 0}
		attr := new(big.Int)
		attr.SetString(fmt.Sprint(system.DexData[id].CaughtAttr), 10)
		if attr.Bit(5) == 0 || attr.Bit(6) == 0 {
			if rule, ok := sourceRules[id]; ok {
				for _, threshold := range candyRules(rule).EggThresholds {
					if system.DexData[id].HatchedCount >= threshold {
						entry.Pity[1] += 3
					}
				}
			}
		}
		copy.StarterData[id] = entry
	}
	return copy
}

// Transfer only qualification and egg provenance. Original runs, teams, matches
// and ranking history stay in their source tables, with their original engine.
func (s *Service) inheritQualification(id []byte, target *State, old defs.SystemSaveData) error {
	if target.Baseline || s.registry == nil || old.GameVersion == s.GameVersion() {
		return nil
	}
	source, ok := s.registry.Lookup(old.GameVersion)
	if !ok || source.BattleVersion() != s.BattleVersion() {
		return nil
	}
	var raw []byte
	if err := s.DB.QueryRow(`SELECT data FROM `+source.accountsTable()+` WHERE uuid=?`, id).Scan(&raw); err != nil {
		if err == sql.ErrNoRows {
			return nil
		}
		return err
	}
	var prior State
	if err := json.Unmarshal(raw, &prior); err != nil {
		return err
	}
	if !prior.Baseline || prior.SystemHashSchema != 2 || prior.SystemHash != systemFingerprint(old) {
		return nil
	}
	for _, job := range prior.EggJobs {
		if job.SourceVersion == "" {
			job.SourceVersion = source.GameVersion()
		}
	}
	prior.Runs = map[string]*Run{}
	prior.Observed = map[string]int{}
	prior.Match = nil
	prior.Audit = nil
	prior.StreakAttempt = nil
	prior.Streak = 0
	prior.Best = 0
	prior.PracticeStreak = 0
	prior.PracticeBest = 0
	prior.Registrations = nil
	// New tickets use the new version's HMAC namespace; pending jobs retain their seeds.
	prior.EggTickets = nil
	prior.EggUsed = prior.EggIssued
	prior.MigratedFrom = source.GameVersion()
	*target = prior
	return nil
}
