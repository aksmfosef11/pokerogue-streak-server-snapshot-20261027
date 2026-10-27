// SPDX-License-Identifier: AGPL-3.0-or-later
package pvp

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const registryControlVersion = "1.12.1.2"

// These are routing controls made from the current export, not evidence of an
// older engine replay. Native per-version engine validation is separate.
func writeRegistryControls(t *testing.T, currentURL, otherURL string) string {
	t.Helper()
	dir := t.TempDir()
	raw, err := os.ReadFile("rules.json")
	if err != nil {
		t.Fatal(err)
	}
	manifestRaw, err := os.ReadFile("rules.manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, version := range []string{Version, registryControlVersion} {
		root := filepath.Join(dir, version)
		if err = os.MkdirAll(root, 0700); err != nil {
			t.Fatal(err)
		}
		data := raw
		var manifest RulesManifest
		if err = json.Unmarshal(manifestRaw, &manifest); err != nil {
			t.Fatal(err)
		}
		if version != Version {
			var rules Rules
			if err = json.Unmarshal(raw, &rules); err != nil {
				t.Fatal(err)
			}
			bulbasaur := rules[1]
			bulbasaur.Cost++
			rules[1] = bulbasaur
			data, err = json.Marshal(rules)
			if err != nil {
				t.Fatal(err)
			}
		}
		manifest.Version = version
		sum := sha256.Sum256(data)
		manifest.RulesSHA256 = hex.EncodeToString(sum[:])
		metadata, err := json.Marshal(manifest)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(root, "rules.json"), data, 0600); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(root, "rules.manifest.json"), metadata, 0600); err != nil {
			t.Fatal(err)
		}
	}
	config := versionRegistryConfig{DefaultVersion: Version, Versions: map[string]versionProfileConfig{
		Version:                {RulesDir: Version, AuditPolicy: Version + ":generation-registry-control", AuditURL: currentURL},
		registryControlVersion: {RulesDir: registryControlVersion, AuditPolicy: registryControlVersion + ":generation-registry-control", AuditURL: otherURL},
	}}
	data, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "versions.json")
	if err = os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PVP_VERSION_REGISTRY", path)
	t.Setenv("PVP_DATA_DIR", filepath.Join(dir, "data"))
	t.Setenv("PVP_AUDIT_WORKERS", "1")
	return path
}

func TestVersionProfilesBindEachExport(t *testing.T) {
	writeRegistryControls(t, "http://current.invalid:8002", "http://other.invalid:8002")
	def, profiles, err := loadVersionProfiles()
	if err != nil {
		t.Fatal(err)
	}
	if def != Version || len(profiles) != 2 {
		t.Fatal("registry entries lost")
	}
	services := map[string]*Service{}
	for i := range profiles {
		p := &profiles[i]
		services[p.Version] = &Service{profile: p, Manifest: p.Manifest, Rules: p.Rules}
	}
	a, b := services[Version], services[registryControlVersion]
	if a.Rules[1].Cost+1 != b.Rules[1].Cost || a.RunContract().RulesSHA256 == b.RunContract().RulesSHA256 {
		t.Fatal("version exports were mixed")
	}
	if b.RunContract().AuditPolicy != b.Policy() || b.Policy() == a.Policy() {
		t.Fatal("policy was not selected per version")
	}
	r := &Registry{defaultVersion: def, services: services}
	if got, ok := r.Lookup(""); !ok || got != a {
		t.Fatal("default lookup failed")
	}
	if _, ok := r.Lookup("unregistered"); ok {
		t.Fatal("unregistered version was routed")
	}
}

func TestVersionProfileRejectsCrossedOrBrokenMetadata(t *testing.T) {
	for _, kind := range []string{"default", "version", "rules", "policy", "worker", "empty", "limit"} {
		t.Run(kind, func(t *testing.T) {
			path := writeRegistryControls(t, "", "")
			raw, _ := os.ReadFile(path)
			var c versionRegistryConfig
			if err := json.Unmarshal(raw, &c); err != nil {
				t.Fatal(err)
			}
			entry := c.Versions[registryControlVersion]
			switch kind {
			case "default":
				c.DefaultVersion = "unregistered"
			case "version":
				entry.RulesDir = Version
			case "rules":
				if err := os.WriteFile(filepath.Join(filepath.Dir(path), registryControlVersion, "rules.json"), []byte("{}"), 0600); err != nil {
					t.Fatal(err)
				}
			case "policy":
				entry.AuditPolicy = AuditPolicy
			case "worker":
				entry.AuditURL = "file:///worker"
			case "empty":
				c.Versions = map[string]versionProfileConfig{}
			case "limit":
				for i := 0; i < maxVersionProfiles; i++ {
					c.Versions["0.0.0."+string(rune('0'+i))] = entry
				}
			}
			if kind != "empty" {
				c.Versions[registryControlVersion] = entry
			}
			raw, _ = json.Marshal(c)
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			if _, _, err := loadVersionProfiles(); err == nil {
				t.Fatal("invalid registry was accepted")
			}
		})
	}
}

func TestVersionStorageAndSeedsKeepLegacyIdentity(t *testing.T) {
	key := bytes.Repeat([]byte{7}, 32)
	a := &Service{Secret: key, profile: &VersionProfile{Version: legacyStorageVersion}}
	b := &Service{Secret: key, profile: &VersionProfile{Version: registryControlVersion}}
	h := hmac.New(sha256.New, key)
	h.Write([]byte("legacy-match:0"))
	if a.decisionSeed("legacy-match", 0) != hex.EncodeToString(h.Sum(nil)) {
		t.Fatal("existing match seeds changed")
	}
	if a.accountsTable() != "pvpAccounts" || a.teamsTable() != "pvpTeams" {
		t.Fatal("existing storage was remapped")
	}
	if b.accountsTable() == a.accountsTable() || b.teamsTable() == a.teamsTable() || len(b.accountsTable()) > 64 {
		t.Fatal("version storage collided")
	}
	if a.decisionSeed("same", 0) == b.decisionSeed("same", 0) || a.eggToken([]byte{1}, 0, 1, "id") == b.eggToken([]byte{1}, 0, 1, "id") {
		t.Fatal("seed domains crossed game versions")
	}
}

func TestRegistrySharesAuditAndQueueCapacity(t *testing.T) {
	var running, maximum atomic.Int32
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var job AuditJob
		if err := json.NewDecoder(r.Body).Decode(&job); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		n := running.Add(1)
		for old := maximum.Load(); n > old; old = maximum.Load() {
			if maximum.CompareAndSwap(old, n) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		running.Add(-1)
		_ = json.NewEncoder(w).Encode(AuditResult{Policy: job.Manifest.Version + ":generation-registry-control", OK: true, PartyVerified: true, InventoryVerified: true, FormItemsVerified: true, LevelsVerified: true, MoneyVerified: true})
	}))
	defer worker.Close()
	registry := &Registry{auditSlots: make(chan struct{}, 1), queueSlots: make(chan struct{}, 64)}
	services := []*Service{}
	for _, v := range []string{Version, registryControlVersion} {
		services = append(services, &Service{profile: &VersionProfile{Version: v, AuditPolicy: v + ":generation-registry-control"}, Manifest: RulesManifest{Version: v}, registry: registry, ctx: context.Background(), auditURL: worker.URL, queue: make(chan []byte, 64), auditActive: map[string]bool{}})
	}
	var group sync.WaitGroup
	for _, s := range services {
		for i := 0; i < 3; i++ {
			group.Add(1)
			go func(s *Service) {
				defer group.Done()
				if result, err := s.verifyGeneration(AuditJob{Run: &Run{Version: s.GameVersion()}, Manifest: s.Manifest}); err != nil || !result.OK {
					t.Errorf("wrong worker route: %+v %v", result, err)
				}
			}(s)
		}
	}
	group.Wait()
	if maximum.Load() != 1 {
		t.Fatalf("registry exceeded the global worker cap: %d", maximum.Load())
	}
	for _, s := range services {
		for i := 0; i < 100; i++ {
			s.enqueueAudit([]byte{byte(i)})
		}
	}
	if len(registry.queueSlots) != 64 || len(services[0].queue)+len(services[1].queue) != 64 {
		t.Fatal("registry queue limit was applied separately per version")
	}
}

func TestOtherVersionWorkerCannotCertifyCurrentProfile(t *testing.T) {
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(AuditResult{Policy: AuditPolicy, OK: true, PartyVerified: true, InventoryVerified: true, FormItemsVerified: true, LevelsVerified: true, MoneyVerified: true})
	}))
	defer worker.Close()
	s := &Service{profile: &VersionProfile{Version: registryControlVersion, AuditPolicy: registryControlVersion + ":generation-registry-control"}, auditURL: worker.URL}
	if result, err := s.verifyGeneration(AuditJob{Run: &Run{Version: registryControlVersion}}); err == nil || result.OK {
		t.Fatal("wrong-version worker certified the job")
	}
}
