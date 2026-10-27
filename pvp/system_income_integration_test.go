// SPDX-License-Identifier: AGPL-3.0-or-later
package pvp

import (
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"
	"time"
)

func TestNativeIncomeReserveUpgradeIntegration(t *testing.T) {
	dsn := os.Getenv("PVP_INTEGRATION_DSN")
	if dsn == "" {
		t.Skip("set PVP_INTEGRATION_DSN for the local Compose database")
	}
	database, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	rules, samples := nativeIncomeControls(t)
	var breeder *nativeIncomeSample
	for i := range samples {
		if samples[i].Name == "breeder-thirteen-eggs" {
			breeder = &samples[i]
			break
		}
	}
	if breeder == nil {
		t.Fatal("native breeder control missing")
	}
	idHex := randomID()
	id, _ := hex.DecodeString(idHex)
	if _, err = database.Exec(`INSERT INTO accounts(uuid,username,hash,salt,registered) VALUES(?,?,?,?,UTC_TIMESTAMP())`, id, "pvin_"+idHex[:10], make([]byte, 32), make([]byte, 16)); err != nil {
		t.Fatal(err)
	}
	defer database.Exec(`DELETE FROM accounts WHERE uuid=?`, id)
	t.Setenv("PVP_RULES_DIR", ".")
	t.Setenv("PVP_AUDIT_URL", "http://127.0.0.1:1")
	first, err := New(database)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if err = first.update(id, func(st *State) error {
		st.Baseline = true
		st.TrustSchema = 1
		st.SystemHashSchema = 2
		st.SystemHash = systemFingerprint(breeder.Old)
		st.LastWaveAt = time.Now().Unix()
		st.Income = &IncomeBudget{Initialized: true}
		st.Income.grant(5, rules)
		st.Income.EventEggs = 5 // exact pre-upgrade cap; version field absent
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err = first.ObserveSystem(id, breeder.Old, breeder.Next, nil); err != nil {
		t.Fatal(err)
	}
	read := func() State {
		var raw []byte
		var st State
		if err = database.QueryRow(`SELECT data FROM pvpAccounts WHERE uuid=?`, id).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal(raw, &st); err != nil {
			t.Fatal(err)
		}
		return st
	}
	st := read()
	if len(st.Flags) != 0 || st.Income.EventEggVersion != eventEggBudgetVersion || st.Income.EventEggs != 52 {
		t.Fatalf("legacy reserve rejected/lost native reward: %+v", st)
	}
	if err = first.update(id, func(st *State) error { st.Income.EventEggs = 0; return nil }); err != nil {
		t.Fatal(err)
	}
	first.Close()
	second, err := New(database)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if err = second.ObserveSystem(id, breeder.Next, breeder.Next, nil); err != nil {
		t.Fatal(err)
	}
	st = read()
	if len(st.Flags) != 0 || st.Income.EventEggs != 0 || st.Income.EventEggVersion != eventEggBudgetVersion {
		t.Fatal("service recreation replenished consumed native reward credit")
	}
	t.Log("persisted legacy reserve accepted native 13 eggs without new waves, and did not refill after service recreation")
}
