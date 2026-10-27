// SPDX-License-Identifier: AGPL-3.0-or-later
package pvp

import (
	"bytes"
	"encoding/gob"
	"github.com/pagefaultgames/rogueserver/defs"
	"testing"
)

func TestTrustedStarterSnapshotDoesNotFollowRawMutation(t *testing.T) {
	data := defs.SystemSaveData{StarterData: defs.StarterData{1: {ValueReduction: 1, PassiveAttr: 1, EggMoves: 2}}}
	state := State{VerifiedEggMoves: map[int]int{1: 2}}
	trustStarters(&state, data)
	originalHash := systemFingerprint(data)
	data.StarterData[1] = defs.StarterEntry{ValueReduction: 2, PassiveAttr: 3, EggMoves: 15}
	if state.TrustedStarters[1].ValueReduction != 1 || state.TrustedStarters[1].EggMoves != 2 {
		t.Fatal("trusted state aliases client data")
	}
	if systemFingerprint(data) == originalHash {
		t.Fatal("unvalidated upgrade did not change fingerprint")
	}
}
func TestSystemFingerprintSurvivesOrdinaryGobStorage(t *testing.T) {
	original := defs.SystemSaveData{TrainerId: 1234, SecretId: 5678, GameVersion: Version, StarterData: defs.StarterData{1: {}}, DexData: defs.DexData{}, Eggs: []defs.EggData{}, EggPity: []int{}, VoucherCounts: defs.VoucherCounts{}}
	var buffer bytes.Buffer
	if err := gob.NewEncoder(&buffer).Encode(original); err != nil {
		t.Fatal(err)
	}
	var restored defs.SystemSaveData
	if err := gob.NewDecoder(&buffer).Decode(&restored); err != nil {
		t.Fatal(err)
	}
	if systemFingerprint(original) != systemFingerprint(restored) {
		t.Fatal("storage encoding changed qualification fingerprint")
	}
}
func TestSystemFingerprintIgnoresMapInsertionOrder(t *testing.T) {
	a := defs.SystemSaveData{StarterData: defs.StarterData{1: {CandyCount: 1}, 4: {CandyCount: 2}}}
	b := defs.SystemSaveData{StarterData: defs.StarterData{4: {CandyCount: 2}, 1: {CandyCount: 1}}}
	if systemFingerprint(a) == "" || systemFingerprint(a) != systemFingerprint(b) {
		t.Fatal("fingerprint is unstable")
	}
}
