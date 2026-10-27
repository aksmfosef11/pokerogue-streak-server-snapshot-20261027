// SPDX-License-Identifier: AGPL-3.0-or-later
package pvp

import (
	"bytes"
	"compress/gzip"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func compressed(data string) string {
	var b bytes.Buffer
	z := gzip.NewWriter(&b)
	z.Write([]byte(data))
	z.Close()
	return "gz:" + base64.StdEncoding.EncodeToString(b.Bytes())
}
func TestAuditRetryBackoff(t *testing.T) {
	if auditRetryDelay(1) != 30*time.Second || auditRetryDelay(2) != time.Minute || auditRetryDelay(10000) != 10*time.Minute {
		t.Fatal("unbounded or missing retry backoff")
	}
}
func TestCompressedLogBoundsAndIntegrity(t *testing.T) {
	data := `{"wave":1,"party":[]}`
	out, err := DecodeLog(compressed(data))
	if err != nil || string(out) != data {
		t.Fatalf("roundtrip: %s %v", out, err)
	}
	// Native 210-stack parties with affordable rerolls exceed the previous cap.
	large := strings.Repeat("native shop snapshot ", 32768)
	if out, err := DecodeLog(compressed(large)); err != nil || string(out) != large {
		t.Fatalf("large native record rejected: %v", err)
	}
	for _, input := range []string{"gz:invalid", "gz:" + base64.StdEncoding.EncodeToString([]byte("plain")), compressed(strings.Repeat("x", MaxAuditWaveBytes+1)), strings.Repeat("x", MaxAuditWaveBytes+1)} {
		if _, err := DecodeLog(input); err == nil {
			t.Fatal("invalid/expanded log accepted")
		}
	}
	if digest("", data) == digest("", compressed(data)) {
		t.Fatal("hash must cover transmitted representation")
	}
}
func TestAuditBridgeAuthVersionAndRejection(t *testing.T) {
	key := []byte("unit-test-key")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := hmac.New(sha256.New, key)
		h.Write([]byte("pvp-audit-v1"))
		if r.Header.Get("Authorization") != "Bearer "+hex.EncodeToString(h.Sum(nil)) {
			t.Error("missing bridge authentication")
		}
		var job AuditJob
		if json.NewDecoder(r.Body).Decode(&job) != nil || job.Run.Version != Version {
			t.Error("job version lost")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":false,"policy":"` + AuditPolicy + `","reason":"wave 2: generated encounter mismatch","elapsedMs":42,"maxRSS":1024}`))
	}))
	defer server.Close()
	s := &Service{Secret: key, auditURL: server.URL}
	result, err := s.verifyGeneration(AuditJob{Run: &Run{Version: Version}})
	if err != nil || result.OK || result.ElapsedMS != 42 || !strings.Contains(result.Reason, "wave 2") {
		t.Fatalf("rejection lost: %+v %v", result, err)
	}
}
func TestUnavailableAuditNeverApprovesBounds(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	defer server.Close()
	for _, url := range []string{"", server.URL} {
		s := &Service{Secret: []byte("key"), auditURL: url}
		result, err := s.verifyGeneration(AuditJob{Run: &Run{Version: Version}})
		if err == nil || result.OK {
			t.Fatal("unavailable audit approved")
		}
	}
}

func TestAuditBridgeRequiresPartyProofOnlyForRegistration(t *testing.T) {
	for _, tc := range []struct {
		name, kind, body string
		accepted, retry  bool
	}{
		{"legacy approval", "", `{"ok":true}`, false, true},
		{"party without inventory", "", `{"ok":true,"partyVerified":true}`, false, true},
		{"inventory without party", "", `{"ok":true,"inventoryVerified":true}`, false, true},
		{"party and inventory without forms", "", `{"ok":true,"partyVerified":true,"inventoryVerified":true}`, false, true},
		{"party inventory and forms without levels", "", `{"ok":true,"partyVerified":true,"inventoryVerified":true,"formItemsVerified":true}`, false, true},
		{"proofs without policy", "", `{"ok":true,"partyVerified":true,"inventoryVerified":true,"formItemsVerified":true,"levelsVerified":true}`, false, true},
		{"wrong policy", "", `{"ok":true,"policy":"obsolete","partyVerified":true,"inventoryVerified":true,"formItemsVerified":true,"levelsVerified":true}`, false, true},
		{"missing money proof at matching policy", "", `{"ok":true,"policy":"` + AuditPolicy + `","partyVerified":true,"inventoryVerified":true,"formItemsVerified":true,"levelsVerified":true}`, false, true},
		{"verified party inventory forms levels and money", "", `{"ok":true,"policy":"` + AuditPolicy + `","partyVerified":true,"inventoryVerified":true,"formItemsVerified":true,"levelsVerified":true,"moneyVerified":true}`, true, false},
		{"missing proof at matching policy", "", `{"ok":true,"policy":"` + AuditPolicy + `"}`, false, true},
		{"old worker rejection", "", `{"ok":false,"reason":"unsupported old worker input"}`, false, true},
		{"rejected input", "", `{"ok":false,"policy":"` + AuditPolicy + `","reason":"invalid input"}`, false, false},
		{"egg approval", "eggs", `{"ok":true}`, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(tc.body)) }))
			defer server.Close()
			s := &Service{Secret: []byte("unit-test"), auditURL: server.URL}
			result, err := s.verifyGeneration(AuditJob{Kind: tc.kind})
			if (err != nil) != tc.retry || result.OK != tc.accepted {
				t.Fatalf("unexpected worker approval: %+v %v", result, err)
			}
		})
	}
}
func TestAuditQueueIsBoundedAndDeduplicates(t *testing.T) {
	s := &Service{queue: make(chan []byte, 1), auditActive: map[string]bool{}}
	s.enqueueAudit([]byte{1})
	s.enqueueAudit([]byte{1})
	s.enqueueAudit([]byte{2})
	if len(s.queue) != 1 || len(s.auditActive) != 1 {
		t.Fatal("queue overflow or duplicate job")
	}
	<-s.queue
	s.auditMu.Lock()
	delete(s.auditActive, "01")
	s.auditMu.Unlock()
	s.enqueueAudit([]byte{2})
	if len(s.queue) != 1 {
		t.Fatal("durable retry could not be queued")
	}
}

func TestGenerationClockCannotSelectAnUnobservedEventDate(t *testing.T) {
	r := &Run{Created: 1700000000, ClearedAt: 1700001200, Anchors: []Anchor{{Wave: 5, At: 1700000010}, {Wave: 200, At: 1700001200}}}
	for _, tc := range []struct {
		name         string
		wave         int
		delta, prior int64
		want         bool
	}{
		{"ordinary", 1, 2000, 0, true},
		{"clock skew", 1, -300000, 0, true},
		{"offline upload", 6, 600000, 2000, true},
		{"historical event", 1, -300001, 0, false},
		{"after committed anchor", 5, 310001, 0, false},
		{"after clear", 200, 1500001, 0, false},
		{"clock rollback", 100, 1000, 600000, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateGenerationTime(r, tc.wave, r.Created*1000+tc.delta, func() int64 {
				if tc.prior == 0 {
					return 0
				}
				return r.Created*1000 + tc.prior
			}())
			if (err == nil) != tc.want {
				t.Fatalf("want allowed=%v: %v", tc.want, err)
			}
		})
	}
	r.ClearedAt = 0
	if validateGenerationTime(r, 1, r.Created*1000, 0) == nil {
		t.Fatal("legacy run without server clear time accepted")
	}
}
