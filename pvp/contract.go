// SPDX-License-Identifier: AGPL-3.0-or-later
package pvp

import "fmt"

// RunContract detects incompatible cached clients before issuing a ranked seed.
// It describes the log format and verifier; proof validation still runs at registration.
type RunContract struct {
	AuditPolicy string `json:"auditPolicy"`
	RulesSHA256 string `json:"rulesSHA256"`
	AuditSchema int    `json:"auditSchema"`
	Harness     string `json:"harness"`
}

func (s *Service) RunContract() RunContract {
	return RunContract{AuditPolicy: s.Policy(), RulesSHA256: s.Manifest.RulesSHA256, AuditSchema: s.Manifest.AuditSchema, Harness: s.Manifest.Harness}
}

func (s *Service) ValidateRunContract(client *RunContract) error {
	if client == nil || *client != s.RunContract() {
		return fmt.Errorf("연승전 검증 규칙이 변경되었습니다. 게임 화면을 새로고침하세요")
	}
	return nil
}
