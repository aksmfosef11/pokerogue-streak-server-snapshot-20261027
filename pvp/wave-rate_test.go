// SPDX-License-Identifier: AGPL-3.0-or-later
package pvp

import (
	"encoding/json"
	"os"
	"testing"
)

func TestNativeRunAwayArrivalTrace(t *testing.T) {
	raw, err := os.ReadFile("testdata/wave-rate-headless.json")
	if err != nil {
		t.Fatal(err)
	}
	var proof struct {
		Manifest RulesManifest `json:"manifest"`
		Samples  []struct {
			Waves     int     `json:"waves"`
			ElapsedMS float64 `json:"elapsedMs"`
		} `json:"samples"`
	}
	if err = json.Unmarshal(raw, &proof); err != nil {
		t.Fatal(err)
	}
	if proof.Manifest.Version != Version || len(proof.Samples) < 3 {
		t.Fatal("native wave timing fixture is missing its version or samples")
	}
	for _, acceleration := range []float64{1, 100} {
		st := State{LastWaveAt: 1700000000, WaveCredit: 10}
		elapsed := float64(0)
		waves := 0
		anchorRejected := false
		for repeat := 0; repeat < 4; repeat++ {
			for _, sample := range proof.Samples {
				elapsed += sample.ElapsedMS / acceleration
				waves += sample.Waves
				st.observeWaveArrival(1700000000+int64(elapsed/1000), sample.Waves)
				if !waveArrivalAllowed(1700000000, 1700000000+int64(elapsed/1000), waves) {
					anchorRejected = true
				}
			}
		}
		if acceleration == 1 && len(st.Flags) != 0 {
			t.Fatalf("native Run Away trace was falsely held: %v", st.Flags)
		}
		if acceleration > 1 && len(st.Flags) == 0 {
			t.Fatal("100x compressed wave arrivals exceeded the finite clock budget")
		}
		if anchorRejected != (acceleration > 1) {
			t.Fatal("anchor arrival gate disagrees with the native/compressed trace")
		}
	}
}

func TestWaveArrivalBudgetSurvivesUploadsAndClockRollback(t *testing.T) {
	st := State{LastWaveAt: 1700000000, WaveCredit: initialWaveCredit}
	for index := 0; index < 2; index++ {
		st.observeWaveArrival(1700000000, 5)
		raw, _ := json.Marshal(st)
		st = State{}
		if err := json.Unmarshal(raw, &st); err != nil {
			t.Fatal(err)
		}
	}
	if st.WaveCredit != 0 || len(st.Flags) != 0 {
		t.Fatal("saved burst credit was reset or not consumed")
	}
	st.observeWaveArrival(1699999999, 0)
	st.observeWaveArrival(1700000000, 1)
	if st.WaveCredit != 0 || len(st.Flags) == 0 || st.LastWaveAt != 1700000000 {
		t.Fatal("repeated uploads/clock rollback recharged the bucket")
	}
	if waveArrivalAllowed(1700000000, 1699999999, 1) {
		t.Fatal("anchor before the server-issued run was accepted")
	}
}
