// SPDX-License-Identifier: AGPL-3.0-or-later
package pvp

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/pagefaultgames/rogueserver/defs"
	"sort"
	"time"
)

type EggTicket struct {
	ID       string `json:"id"`
	Sequence int    `json:"sequence"`
	Seed     string `json:"seed"`
	Issued   int64  `json:"issued"`
}
type EggCertificate struct {
	Egg          defs.EggData `json:"egg"`
	Sequence     int          `json:"sequence"`
	Epoch        int          `json:"epoch"`
	Status       string       `json:"status"`
	HatchSpecies int          `json:"hatchSpecies,omitempty"`
	HatchBatch   string       `json:"hatchBatch,omitempty"`
	Consumed     bool         `json:"consumed,omitempty"`
}
type EggAuditJob struct {
	SourceVersion string               `json:"sourceVersion,omitempty"`
	ID            string               `json:"id"`
	Epoch         int                  `json:"epoch"`
	Receipts      []defs.EggProof      `json:"receipts"`
	Seeds         map[int]string       `json:"seeds"`
	Basis         defs.EggProofContext `json:"basis"`
	Next          defs.EggProofContext `json:"next"`
	Attempts      int                  `json:"attempts"`
	NextAttempt   int64                `json:"nextAttempt"`
}
type EggHatchBatch struct {
	ID      string      `json:"id"`
	Credits map[int]int `json:"credits"`
	Eggs    []int       `json:"eggs"`
}
type VerifiedEgg struct {
	ID        int `json:"id"`
	Species   int `json:"species"`
	MoveIndex int `json:"moveIndex"`
}

func initEggTrust(st *State) {
	if st.EggTrustSchema == 0 {
		st.VerifiedEggMoves = map[int]int{}
		for id, p := range st.TrustedStarters {
			if p.EggMoves != 0 && len(st.Reviews) == 0 {
				st.flag("egg-baseline-review-required")
			}
			if len(st.Reviews) > 0 {
				st.VerifiedEggMoves[id] = p.EggMoves
			}
		}
		st.EggTrustSchema = 1
	}
	if st.VerifiedEggMoves == nil {
		st.VerifiedEggMoves = map[int]int{}
	}
	if st.EggCertificates == nil {
		st.EggCertificates = map[int]*EggCertificate{}
	}
	if st.EggRecent == nil {
		st.EggRecent = map[int]string{}
	}
	if st.TrustedStarters == nil {
		st.TrustedStarters = map[int]TrustedStarter{}
	}
}
func (s *Service) eggToken(id []byte, epoch, sequence int, kind string) string {
	h := hmac.New(sha256.New, s.Secret)
	if s.GameVersion() != legacyStorageVersion {
		fmt.Fprintf(h, "pvp-version-v1:%s:", s.GameVersion())
	}
	fmt.Fprintf(h, "egg-v1:%x:%d:%d:%s", id, epoch, sequence, kind)
	return hex.EncodeToString(h.Sum(nil))
}
func (s *Service) EggTicketsFor(id []byte) (Object, error) {
	var out Object
	err := s.update(id, func(st *State) error {
		initEggTrust(st)
		now := time.Now().Unix()
		if st.EggIssuedAt/86400 != now/86400 {
			st.EggIssuedToday = 0
			st.EggIssuedAt = now
		}
		for len(st.Flags) == 0 && len(st.EggTickets) < 64 && st.EggIssuedToday < 8192 && len(st.EggJobs) < 16 && len(st.EggCertificates)+len(st.EggTickets) < 256 {
			st.EggIssued++
			st.EggIssuedToday++
			st.EggTickets = append(st.EggTickets, EggTicket{ID: s.eggToken(id, st.TrustEpoch, st.EggIssued, "id")[:32], Sequence: st.EggIssued, Seed: s.eggToken(id, st.TrustEpoch, st.EggIssued, "seed")[:24], Issued: time.Now().UnixMilli()})
		}
		ids := []int{}
		for id, c := range st.EggCertificates {
			if c.Status == "verified" {
				ids = append(ids, id)
			}
		}
		sort.Ints(ids)
		tickets := st.EggTickets
		if len(st.Flags) > 0 {
			tickets = []EggTicket{}
		}
		out = Object{"epoch": st.TrustEpoch, "usedThrough": st.EggUsed, "tickets": tickets, "certifiedIds": ids}
		return nil
	})
	return out, err
}
func proofContext(system defs.SystemSaveData) defs.EggProofContext {
	c := defs.EggProofContext{EggPity: system.EggPity, UnlockPity: system.UnlockPity, Caught: []int{}, Eggs: []defs.EggProofStock{}}
	nativePity := false
	for _, entry := range system.StarterData {
		if len(entry.Pity) > 0 {
			nativePity = true
			break
		}
	}
	if nativePity {
		c.StarterPity = map[int][]int{}
		c.CaughtAttrs = map[int]string{}
		for id, entry := range system.StarterData {
			c.StarterPity[id] = append([]int(nil), entry.Pity...)
		}
	}
	for id, d := range system.DexData {
		if attrSet(d.CaughtAttr) {
			c.Caught = append(c.Caught, id)
			if nativePity {
				c.CaughtAttrs[id] = fmt.Sprint(d.CaughtAttr)
			}
		}
	}
	sort.Ints(c.Caught)
	for _, e := range system.Eggs {
		stock := defs.EggProofStock{ID: e.Id, Species: e.Species}
		if nativePity {
			v := e.VariantTier
			stock.VariantTier = &v
		}
		c.Eggs = append(c.Eggs, stock)
	}
	return c
}
func attrSet(v any) bool { return v != nil && fmt.Sprint(v) != "0" && fmt.Sprint(v) != "" }
func proofHash(p defs.EggProof) string {
	raw, _ := json.Marshal(p)
	h := sha256.Sum256(raw)
	return hex.EncodeToString(h[:])
}
func (s *Service) observeEggProofs(st *State, old, next defs.SystemSaveData) {
	initEggTrust(st)
	old = s.eggBasis(old)
	pityChanged := false
	if s.GameVersion() == "1.12.2.0" {
		for id, entry := range next.StarterData {
			if len(entry.Pity) != 2 || entry.Pity[0] < 0 || entry.Pity[1] < 0 || entry.Pity[0] > 1000000 || entry.Pity[1] > 1000000 {
				st.flag("same-species-pity-range")
				break
			}
			prior := old.StarterData[id].Pity
			if len(prior) == 0 {
				prior = []int{0, 0}
			}
			if len(prior) != 2 || entry.Pity[0] != prior[0] || entry.Pity[1] != prior[1] {
				pityChanged = true
			}
		}
		if pityChanged && len(next.PVPEggProofs) == 0 {
			st.flag("same-species-pity-unproven")
		}
	}
	st.CurrentEggMoves = map[int]int{}
	for id, p := range next.StarterData {
		st.CurrentEggMoves[id] = p.EggMoves
	}
	if len(st.Flags) > 0 {
		return
	}
	if len(next.PVPEggProofs) > 128 {
		st.flag("egg-proof-count")
		return
	}
	newProofs := []defs.EggProof{}
	seeds := map[int]string{}
	stock := map[int]bool{}
	actual := map[int]defs.EggData{}
	for _, e := range next.Eggs {
		stock[e.Id] = true
		actual[e.Id] = e
	}
	prior := map[int]bool{}
	for _, e := range old.Eggs {
		prior[e.Id] = true
	}
	for _, p := range next.PVPEggProofs {
		if p.Sequence == 0 {
			if !prior[p.Egg.Id] && stock[p.Egg.Id] {
				newProofs = append(newProofs, p)
			}
			continue
		}
		if p.Sequence <= st.EggUsed {
			if hash, ok := st.EggRecent[p.Sequence]; ok && hash != proofHash(p) {
				st.flag("egg-ticket-reused")
			}
			continue
		}
		if len(st.EggTickets) == 0 || st.EggTickets[0].Sequence != p.Sequence || st.EggTickets[0].ID != p.TicketID {
			st.flag("egg-ticket-order")
			continue
		}
		ticket := st.EggTickets[0]
		st.EggTickets = st.EggTickets[1:]
		st.EggUsed = p.Sequence
		st.EggRecent[p.Sequence] = proofHash(p)
		for seq := range st.EggRecent {
			if seq < st.EggUsed-128 {
				delete(st.EggRecent, seq)
			}
		}
		// Upload may follow offline play. Bound creation by ticket issuance and
		// arrival, rather than rejecting a genuine egg merely for a delayed save.
		if p.Egg.Timestamp < int(ticket.Issued)-300000 || int64(p.Egg.Timestamp) > time.Now().UnixMilli()+300000 || p.Options.Timestamp != p.Egg.Timestamp || !stock[p.Egg.Id] || prior[p.Egg.Id] || st.EggCertificates[p.Egg.Id] != nil {
			st.flag("egg-proof-origin")
			continue
		}
		copy := p.Egg
		copy.HatchWaves = actual[p.Egg.Id].HatchWaves
		if copy != actual[p.Egg.Id] || p.Egg.HatchWaves < copy.HatchWaves {
			st.flag("egg-proof-inventory")
			continue
		}
		seeds[p.Sequence] = ticket.Seed
		newProofs = append(newProofs, p)
		st.EggCertificates[p.Egg.Id] = &EggCertificate{Egg: p.Egg, Sequence: p.Sequence, Epoch: st.TrustEpoch, Status: "pending"}
	}
	if len(seeds) > 0 || pityChanged && len(newProofs) > 0 {
		if len(st.EggJobs) >= 80 || len(st.EggCertificates) > 256 {
			st.flag("egg-audit-backlog")
		} else {
			basis := proofContext(old)
			last := (*EggAuditJob)(nil)
			if len(st.EggJobs) > 0 {
				last = st.EggJobs[len(st.EggJobs)-1]
			}
			before, _ := json.Marshal(basis)
			previous := []byte(nil)
			if last != nil {
				previous, _ = json.Marshal(last.Next)
			}
			if last != nil && last.SourceVersion == "" && last.Attempts == 0 && len(last.Receipts)+len(newProofs) <= 128 && string(before) == string(previous) {
				last.Receipts = append(last.Receipts, newProofs...)
				for seq, seed := range seeds {
					last.Seeds[seq] = seed
				}
				last.Next = proofContext(next)
			} else {
				st.EggJobs = append(st.EggJobs, &EggAuditJob{ID: randomID(), Epoch: st.TrustEpoch, Receipts: newProofs, Seeds: seeds, Basis: basis, Next: proofContext(next)})
			}
		}
	}
	batch := &EggHatchBatch{ID: randomID(), Credits: map[int]int{}}
	for id, n := range next.DexData {
		batch.Credits[id] = max(0, n.HatchedCount-old.DexData[id].HatchedCount)
	}
	for _, e := range old.Eggs {
		c := st.EggCertificates[e.Id]
		if c == nil || c.Consumed || c.HatchBatch != "" || stock[e.Id] {
			continue
		}
		c.HatchBatch = batch.ID
		batch.Eggs = append(batch.Eggs, e.Id)
	}
	if len(batch.Eggs) > 0 {
		st.EggHatches = append(st.EggHatches, batch)
	}
	applyEggHatches(st)
}

func (s *Service) auditEggOne(id []byte) error {
	var job *EggAuditJob
	err := s.update(id, func(st *State) error {
		if len(st.EggJobs) == 0 || st.EggJobs[0].NextAttempt > time.Now().Unix() {
			return nil
		}
		job = st.EggJobs[0]
		job.Attempts++
		job.NextAttempt = time.Now().Add(auditRetryDelay(job.Attempts)).Unix()
		return nil
	})
	if err != nil || job == nil {
		return err
	}
	verifier := s
	if job.SourceVersion != "" && job.SourceVersion != s.GameVersion() {
		if s.registry == nil {
			return fmt.Errorf("egg source engine unavailable")
		}
		var ok bool
		verifier, ok = s.registry.Lookup(job.SourceVersion)
		if !ok {
			return fmt.Errorf("egg source engine unavailable")
		}
	}
	result, err := verifier.verifyGeneration(AuditJob{Kind: "eggs", EggJob: job, Manifest: verifier.Manifest})
	if err != nil {
		return err
	}
	return s.update(id, func(st *State) error {
		if len(st.EggJobs) == 0 || st.EggJobs[0].ID != job.ID {
			return nil
		}
		initEggTrust(st)
		if job.Epoch == st.TrustEpoch {
			st.EggAuditError = result.Reason
			if s.GameVersion() == "1.12.2.0" && job.SourceVersion == "" && !result.OK {
				st.flag("egg-proof-mismatch")
			}
			results := map[int]VerifiedEgg{}
			for _, e := range result.Eggs {
				results[e.ID] = e
			}
			for _, p := range job.Receipts {
				if p.Sequence == 0 {
					continue
				}
				c := st.EggCertificates[p.Egg.Id]
				if c == nil || c.Epoch != job.Epoch {
					continue
				}
				e, ok := results[p.Egg.Id]
				if result.OK && ok && e.MoveIndex == c.Egg.EggMoveIndex && e.Species > 0 {
					if c.Status == "pending" {
						st.EggStatistics.verified(c.Egg)
					}
					c.Status = "verified"
					c.HatchSpecies = e.Species
				} else {
					c.Status = "rejected"
					st.flag("egg-proof-mismatch")
				}
			}
			applyEggHatches(st)
		}
		st.EggJobs = st.EggJobs[1:]
		return nil
	})
}
func applyEggHatches(st *State) {
	remaining := []*EggHatchBatch{}
	for _, b := range st.EggHatches {
		pending := false
		for _, id := range b.Eggs {
			c := st.EggCertificates[id]
			if c == nil || c.Consumed {
				continue
			}
			if c.Status == "pending" {
				pending = true
				continue
			}
			if c.Status == "verified" && c.Epoch == st.TrustEpoch && len(st.Flags) == 0 && b.Credits[c.HatchSpecies] > 0 {
				b.Credits[c.HatchSpecies]--
				st.VerifiedEggMoves[c.HatchSpecies] |= 1 << c.Egg.EggMoveIndex
				t := st.TrustedStarters[c.HatchSpecies]
				t.EggMoves = st.VerifiedEggMoves[c.HatchSpecies]
				st.TrustedStarters[c.HatchSpecies] = t
			}
			c.Consumed = true
		}
		if pending {
			remaining = append(remaining, b)
		} else {
			for _, id := range b.Eggs {
				delete(st.EggCertificates, id)
			}
		}
	}
	st.EggHatches = remaining
}
