// SPDX-License-Identifier: AGPL-3.0-or-later
package pvp

import (
	"encoding/json"
	"os"
	"testing"
)

func TestRankedRunContract(t *testing.T) {
	raw, err := os.ReadFile("rules.manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	var manifest RulesManifest
	if err = json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	s := &Service{Manifest: manifest}
	current := s.RunContract()
	if current.AuditPolicy != AuditPolicy || current.RulesSHA256 != manifest.RulesSHA256 || current.AuditSchema != manifest.AuditSchema || current.Harness != manifest.Harness {
		t.Fatalf("contract did not bind the deployed rules: %+v", current)
	}
	if err := s.ValidateRunContract(&current); err != nil {
		t.Fatal(err)
	}
	if err := s.ValidateRunContract(nil); err == nil {
		t.Fatal("legacy client started without a contract")
	}
	for _, name := range []string{"policy", "rules", "schema", "harness"} {
		t.Run(name, func(t *testing.T) {
			other := current
			switch name {
			case "policy":
				other.AuditPolicy = Version + ":generation-v14"
			case "rules":
				other.RulesSHA256 = "other-export"
			case "schema":
				other.AuditSchema++
			case "harness":
				other.Harness = "other/audit.ts"
			}
			if err := s.ValidateRunContract(&other); err == nil {
				t.Fatal("incompatible client was accepted")
			}
		})
	}
}
