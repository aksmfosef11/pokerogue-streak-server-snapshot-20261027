// SPDX-License-Identifier: AGPL-3.0-or-later
package pvp

import "time"

// The native five-wave Run Away samples take ~301 ms/wave without visuals or
// network (testdata/wave-rate-headless.json). Allow three times that throughput
// and retain the ten-wave burst for whole-second arrival timestamps. This is a
// coarse income/anchor bound, not a claim to replay campaign combat or UI time.
const minimumObservedWaveInterval = 100 * time.Millisecond
const waveArrivalPerSecond = int(time.Second / minimumObservedWaveInterval)
const initialWaveCredit = 10
const maxWaveCredit = 86400

func waveArrivalRefill(seconds int64) int {
	return min(maxWaveCredit, int(min(int64(maxWaveCredit), max(0, seconds)))*waveArrivalPerSecond)
}

func waveArrivalAllowed(created, arrived int64, wave int) bool {
	return arrived >= created && wave <= initialWaveCredit+waveArrivalRefill(arrived-created)
}

func (st *State) observeWaveArrival(now int64, waves int) {
	// The bucket belongs to the account/profile, not an upload or seed.
	if st.LastWaveAt == 0 {
		st.WaveCredit = initialWaveCredit
	} else {
		st.WaveCredit = min(maxWaveCredit, st.WaveCredit+waveArrivalRefill(now-st.LastWaveAt))
	}
	st.LastWaveAt = max(st.LastWaveAt, now)
	if waves > st.WaveCredit {
		st.flag("wave-arrival-rate")
	}
	st.WaveCredit = max(0, st.WaveCredit-waves)
}
