// SPDX-License-Identifier: AGPL-3.0-or-later
package pvp

import (
	"encoding/json"
	"github.com/pagefaultgames/rogueserver/defs"
)

const Version = "1.12.1.3"
const AuditPolicy = Version + ":generation-v16"

// Limits mirror the pinned client harness. Wire logs remain capped at 64 KiB;
// complete shop snapshots repeat during rerolls and need a larger decoded cap.
const MaxAuditItems = 1024
const MaxAuditWaveBytes = 16 << 20
const MaxAuditRequestBytes = 16 << 20
const MaxAuditLevel = 1000000            // Earned levels are proved by the pinned engine.
const MaxAuditMoney = 1<<53 - 1          // Native wallet cap; the engine checks available income separately.
const MaxResultHP = MaxAuditLevel * 1000 // Structural integer bound, including native high-level held-item stats.

type Object = map[string]any
type Starter struct {
	Species   int   `json:"speciesId"`
	FormIndex int   `json:"formIndex"`
	Passive   bool  `json:"passive"`
	Moves     []int `json:"moveset"`
}
type Rule struct {
	Cost         int           `json:"cost"`
	Root         int           `json:"root"`
	EggTier      int           `json:"eggTier"`
	StarterMoves map[int][]int `json:"starterMoves"`
	EggMoves     []int         `json:"eggMoves"`
	Candy        *CandyCosts   `json:"candy"`
}
type CandyCosts struct {
	FriendshipCap        int   `json:"friendshipCap"`
	FriendshipMultiplier int   `json:"friendshipMultiplier"`
	CatchBonus           int   `json:"catchBonus"`
	Passive              int   `json:"passive"`
	Reduction            []int `json:"reduction"`
	EggCosts             []int `json:"eggCosts"`
	EggThresholds        []int `json:"eggThresholds"`
}
type Rules map[int]Rule
type RulesManifest struct {
	Schema      int    `json:"schema"`
	Version     string `json:"version"`
	AuditSchema int    `json:"auditSchema"`
	Harness     string `json:"harness"`
	RulesSHA256 string `json:"rulesSHA256"`
}
type Anchor struct {
	Wave int    `json:"wave"`
	Head string `json:"head"`
	At   int64  `json:"at"`
}
type Log struct {
	Wave    int    `json:"wave"`
	Payload string `json:"payload"`
	Head    string `json:"head"`
}
type Run struct {
	TrustEpoch int                   `json:"trustEpoch,omitempty"`
	ID         string                `json:"id"`
	Seed       string                `json:"seed"`
	Version    string                `json:"version"`
	Contract   *RunContract          `json:"contract,omitempty"`
	Created    int64                 `json:"created"`
	ClearedAt  int64                 `json:"clearedAt,omitempty"`
	Starters   []Starter             `json:"starters"`
	Identity   Object                `json:"identity"`
	Anchors    []Anchor              `json:"anchors"`
	Candidate  *defs.SessionSaveData `json:"candidate,omitempty"`
}
type Audit struct {
	ID          string `json:"id"`
	RunID       string `json:"runId"`
	Status      string `json:"status"`
	Reason      string `json:"reason,omitempty"`
	Logs        []Log  `json:"logs,omitempty"`
	ElapsedMS   int64  `json:"elapsedMs,omitempty"`
	MaxRSS      int64  `json:"maxRSS,omitempty"`
	Attempts    int    `json:"attempts,omitempty"`
	NextAttempt int64  `json:"nextAttempt,omitempty"`
}
type Decision struct {
	Index  int             `json:"index"`
	Kind   string          `json:"kind"`
	Choice json.RawMessage `json:"choice"`
	Digest string          `json:"digest"`
	Seed   string          `json:"seed"`
}
type Match struct {
	SourceVersion   string     `json:"sourceVersion,omitempty"`
	OpponentVersion string     `json:"opponentVersion,omitempty"`
	BattlePath      string     `json:"battlePath,omitempty"`
	OpponentTeam    string     `json:"opponentTeam,omitempty"`
	ID              string     `json:"id"`
	Practice        bool       `json:"practice"`
	Opponent        string     `json:"opponent"`
	Session         Object     `json:"session"`
	Decisions       []Decision `json:"decisions"`
	Status          string     `json:"status"`
	Expires         int64      `json:"expires"`
	Created         int64      `json:"created"`
}
type State struct {
	MigratedFrom     string                  `json:"migratedFrom,omitempty"`
	StreakAttempt    *StreakAttempt          `json:"streakAttempt,omitempty"`
	EggTrustSchema   int                     `json:"eggTrustSchema,omitempty"`
	VerifiedEggMoves map[int]int             `json:"verifiedEggMoves,omitempty"`
	CurrentEggMoves  map[int]int             `json:"currentEggMoves,omitempty"`
	EggIssued        int                     `json:"eggIssued,omitempty"`
	EggUsed          int                     `json:"eggUsed,omitempty"`
	EggIssuedAt      int64                   `json:"eggIssuedAt,omitempty"`
	EggIssuedToday   int                     `json:"eggIssuedToday,omitempty"`
	EggTickets       []EggTicket             `json:"eggTickets,omitempty"`
	EggRecent        map[int]string          `json:"eggRecent,omitempty"`
	EggCertificates  map[int]*EggCertificate `json:"eggCertificates,omitempty"`
	EggJobs          []*EggAuditJob          `json:"eggJobs,omitempty"`
	EggAuditError    string                  `json:"eggAuditError,omitempty"`
	EggHatches       []*EggHatchBatch        `json:"eggHatches,omitempty"`
	Income           *IncomeBudget           `json:"income,omitempty"`
	EggStatistics    EggStatistics           `json:"eggStatistics"`
	TrustEpoch       int                     `json:"trustEpoch,omitempty"`
	Reviews          []BaselineReview        `json:"reviews,omitempty"`
	Flags            []string                `json:"flags"`
	Baseline         bool                    `json:"baseline"`
	TrustSchema      int                     `json:"trustSchema,omitempty"`
	SystemHash       string                  `json:"systemHash,omitempty"`
	SystemHashSchema int                     `json:"systemHashSchema,omitempty"`
	TrustedStarters  map[int]TrustedStarter  `json:"trustedStarters,omitempty"`
	Runs             map[string]*Run         `json:"runs"`
	Observed         map[string]int          `json:"observed"`
	LastUpload       int64                   `json:"lastUpload"`
	CandyCarry       map[int]int             `json:"candyCarry"`
	WaveCredit       int                     `json:"waveCredit"`
	LastWaveAt       int64                   `json:"lastWaveAt"`
	Audit            *Audit                  `json:"audit,omitempty"`
	Match            *Match                  `json:"match,omitempty"`
	Streak           int                     `json:"streak"`
	Best             int                     `json:"best"`
	PracticeStreak   int                     `json:"practiceStreak"`
	PracticeBest     int                     `json:"practiceBest"`
	Registrations    []int64                 `json:"registrations"`
}
type BaselineReview struct {
	At            int64    `json:"at"`
	Operator      string   `json:"operator"`
	Reason        string   `json:"reason"`
	PreviousFlags []string `json:"previousFlags"`
	PreviousHash  string   `json:"previousHash"`
	NewHash       string   `json:"newHash"`
	Epoch         int      `json:"epoch"`
}
type TrustedStarter struct {
	ValueReduction int `json:"valueReduction"`
	PassiveAttr    int `json:"passiveAttr"`
	EggMoves       int `json:"eggMoves"`
}
type Team struct {
	AuditPolicy string               `json:"auditPolicy"`
	Name        string               `json:"name"`
	RunID       string               `json:"runId"`
	Session     defs.SessionSaveData `json:"session"`
}

func (s *State) flag(reason string) {
	for _, f := range s.Flags {
		if f == reason {
			return
		}
	}
	if len(s.Flags) < 32 {
		s.Flags = append(s.Flags, reason)
	}
}
