// SPDX-License-Identifier: AGPL-3.0-or-later
package pvp

import (
	"database/sql"
	"encoding/json"
	"time"
)

func createRegistrationLimits(db *sql.DB) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS pvpRegistrationLimits (uuid BINARY(16) PRIMARY KEY, data LONGTEXT NOT NULL, FOREIGN KEY (uuid) REFERENCES accounts(uuid) ON DELETE CASCADE)`); err != nil {
		return err
	}
	return nil
}

type registrationAttempt struct {
	Version string `json:"version"`
	At      int64  `json:"at"`
}
type registrationLimit struct {
	Attempts []registrationAttempt `json:"attempts"`
}

func legacyRegistrationHistory(db *sql.DB, id []byte) ([]int64, error) {
	var raw []byte
	if err := db.QueryRow(`SELECT COALESCE(JSON_EXTRACT(data,'$.registrations'),'null') FROM pvpAccounts WHERE uuid=?`, id).Scan(&raw); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	var history []int64
	if err := json.Unmarshal(raw, &history); err != nil {
		return nil, err
	}
	return history, nil
}
func recentRegistrations(values []int64, now int64) []int64 {
	out := []int64{}
	for _, at := range values {
		if at > now-int64((24*time.Hour)/time.Second) {
			out = append(out, at)
		}
	}
	return out
}
func reserveRegistration(tx *sql.Tx, id []byte, version string, now int64, histories map[string][]int64) error {
	var raw []byte
	if err := tx.QueryRow(`SELECT data FROM pvpRegistrationLimits WHERE uuid=? FOR UPDATE`, id).Scan(&raw); err != nil {
		return err
	}
	var limit registrationLimit
	if err := json.Unmarshal(raw, &limit); err != nil {
		return err
	}
	values := []registrationAttempt{}
	for _, a := range limit.Attempts {
		if a.At > now-int64((24*time.Hour)/time.Second) {
			values = append(values, a)
		}
	}
	// Reconcile the legacy history by multiplicity, including two attempts in
	// one second. Repeated starts and later deployments cannot recharge it.
	for name, history := range histories {
		existing := map[int64]int{}
		for _, a := range values {
			if a.Version == name {
				existing[a.At]++
			}
		}
		seen := map[int64]int{}
		for _, at := range recentRegistrations(history, now) {
			seen[at]++
			if seen[at] > existing[at] {
				values = append(values, registrationAttempt{Version: name, At: at})
			}
		}
	}
	limit.Attempts = append(values, registrationAttempt{Version: version, At: now})
	raw, err := json.Marshal(limit)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`UPDATE pvpRegistrationLimits SET data=? WHERE uuid=?`, raw, id)
	return err
}
