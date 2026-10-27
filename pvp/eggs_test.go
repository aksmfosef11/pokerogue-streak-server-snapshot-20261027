package pvp

import (
	"github.com/pagefaultgames/rogueserver/defs"
	"testing"
	"time"
)

func eggProofFixture() (State, defs.SystemSaveData, defs.SystemSaveData) {
	now := int(time.Now().UnixMilli())
	st := State{EggTrustSchema: 1, EggTickets: []EggTicket{{ID: "ticket", Sequence: 1, Seed: "fixed-seed", Issued: int64(now)}}, TrustedStarters: map[int]TrustedStarter{1: {}}}
	egg := defs.EggData{Id: 123, SourceType: 3, Species: 1, HatchWaves: 10, Timestamp: now, EggMoveIndex: 1}
	old := defs.SystemSaveData{StarterData: defs.StarterData{1: {}}, DexData: defs.DexData{}}
	next := old
	next.Eggs = []defs.EggData{egg}
	next.PVPEggProofs = []defs.EggProof{{ID: "receipt", TicketID: "ticket", Sequence: 1, Egg: egg, Options: defs.EggProofOptions{SourceType: 3, Species: 1, Timestamp: now}}}
	return st, old, next
}
func TestEggTicketConsumptionIsDurableAndIdempotent(t *testing.T) {
	st, old, next := eggProofFixture()
	s := &Service{}
	s.observeEggProofs(&st, old, next)
	if st.EggUsed != 1 || len(st.EggTickets) != 0 || len(st.EggJobs) != 1 || st.EggCertificates[123].Status != "pending" || len(st.Flags) > 0 {
		t.Fatalf("%+v", st)
	}
	s.observeEggProofs(&st, next, next)
	if len(st.EggJobs) != 1 || len(st.Flags) > 0 {
		t.Fatalf("duplicate changed state: %+v", st)
	}
	next.PVPEggProofs[0].Egg.EggMoveIndex = 3
	s.observeEggProofs(&st, next, next)
	requireFlag(t, st.Flags, "egg-ticket-reused")
}
func TestEggTicketCannotSkipOrChangeInventory(t *testing.T) {
	st, old, next := eggProofFixture()
	s := &Service{}
	next.PVPEggProofs[0].Sequence = 2
	s.observeEggProofs(&st, old, next)
	requireFlag(t, st.Flags, "egg-ticket-order")
	st, old, next = eggProofFixture()
	next.PVPEggProofs[0].Egg.IsShiny = true
	s.observeEggProofs(&st, old, next)
	requireFlag(t, st.Flags, "egg-proof-inventory")
}

func TestEggTicketAllowsDelayedOfflineUploadButNotPredatingIssuance(t *testing.T) {
	st, old, next := eggProofFixture()
	st.EggTickets[0].Issued -= int64(time.Hour / time.Millisecond)
	next.Eggs[0].Timestamp -= int(time.Hour / time.Millisecond)
	next.PVPEggProofs[0].Egg.Timestamp = next.Eggs[0].Timestamp
	next.PVPEggProofs[0].Options.Timestamp = next.Eggs[0].Timestamp
	(&Service{}).observeEggProofs(&st, old, next)
	if len(st.Flags) != 0 || len(st.EggJobs) != 1 {
		t.Fatalf("delayed upload rejected: %+v", st.Flags)
	}
	st, old, next = eggProofFixture()
	next.Eggs[0].Timestamp -= 600000
	next.PVPEggProofs[0].Egg.Timestamp = next.Eggs[0].Timestamp
	next.PVPEggProofs[0].Options.Timestamp = next.Eggs[0].Timestamp
	(&Service{}).observeEggProofs(&st, old, next)
	requireFlag(t, st.Flags, "egg-proof-origin")
}
func TestRawEggMovesStayUntrustedUntilCertifiedHatch(t *testing.T) {
	st := State{EggTrustSchema: 1, VerifiedEggMoves: map[int]int{}}
	data := defs.SystemSaveData{StarterData: defs.StarterData{1: {EggMoves: 2}}}
	trustStarters(&st, data)
	if st.TrustedStarters[1].EggMoves != 0 {
		t.Fatal("fallback egg move trusted")
	}
	initEggTrust(&st)
	st.CurrentEggMoves = map[int]int{1: 2}
	st.EggCertificates[123] = &EggCertificate{Epoch: 0, Status: "pending", Egg: defs.EggData{EggMoveIndex: 1}, HatchBatch: "batch"}
	st.EggHatches = []*EggHatchBatch{{ID: "batch", Credits: map[int]int{1: 1}, Eggs: []int{123}}}
	applyEggHatches(&st)
	if st.TrustedStarters[1].EggMoves != 0 || len(st.EggHatches) != 1 {
		t.Fatal("pending verification was credited/lost")
	}
	st.EggCertificates[123].Status = "verified"
	st.EggCertificates[123].HatchSpecies = 1
	applyEggHatches(&st)
	if st.TrustedStarters[1].EggMoves != 2 || len(st.EggHatches) != 0 || len(st.EggCertificates) != 0 {
		t.Fatal("verified hatch not credited once")
	}
	applyEggHatches(&st)
	if st.TrustedStarters[1].EggMoves != 2 {
		t.Fatal("hatch replay changed trust")
	}
}
func TestEggTokensBindAccountEpochAndSequence(t *testing.T) {
	s := &Service{Secret: []byte("test-secret")}
	a := s.eggToken([]byte("a"), 1, 1, "seed")
	for _, b := range []string{s.eggToken([]byte("b"), 1, 1, "seed"), s.eggToken([]byte("a"), 2, 1, "seed"), s.eggToken([]byte("a"), 1, 2, "seed"), s.eggToken([]byte("a"), 1, 1, "id")} {
		if a == b {
			t.Fatal("ticket domain separation failed")
		}
	}
}
