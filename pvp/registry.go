// SPDX-License-Identifier: AGPL-3.0-or-later
package pvp

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
)

type Registry struct {
	defaultVersion string
	services       map[string]*Service
	cancel         context.CancelFunc
	once           sync.Once
	auditSlots     chan struct{}
	queueSlots     chan struct{}
}

func NewRegistry(database *sql.DB) (*Registry, error) {
	return newRegistry(database, true)
}

// OpenRegistry is for local review tools; it never resumes queued audits.
func OpenRegistry(database *sql.DB) (*Registry, error) {
	return newRegistry(database, false)
}

func newRegistry(database *sql.DB, startWorkers bool) (*Registry, error) {
	def, profiles, err := loadVersionProfiles()
	if err != nil {
		return nil, err
	}
	secret, err := loadRegistrySecret()
	if err != nil {
		return nil, err
	}
	n, _ := strconv.Atoi(os.Getenv("PVP_AUDIT_WORKERS"))
	n = max(1, min(n, 4))
	ctx, cancel := context.WithCancel(context.Background())
	r := &Registry{defaultVersion: def, services: map[string]*Service{}, cancel: cancel, auditSlots: make(chan struct{}, n), queueSlots: make(chan struct{}, 64)}
	// The original tables remain bound to their original version, even when
	// another entry becomes the default. No existing rows are moved or erased.
	legacy := &Service{DB: database, profile: &VersionProfile{Version: legacyStorageVersion}}
	if err = legacy.createTables(); err != nil {
		cancel()
		return nil, err
	}
	for i := range profiles {
		p := profiles[i]
		s := &Service{DB: database, Secret: secret, Rules: p.Rules, Manifest: p.Manifest, profile: &p, registry: r, Practice: os.Getenv("PVP_ALLOW_PRACTICE") == "true", queue: make(chan []byte, 64), auditURL: p.AuditURL, auditActive: map[string]bool{}, ctx: ctx, cancel: cancel}
		if err = s.createTables(); err != nil {
			cancel()
			return nil, err
		}
		r.services[p.Version] = s
	}
	if err = createRegistrationLimits(database); err != nil {
		cancel()
		return nil, err
	}
	if startWorkers {
		for _, s := range r.services {
			s.startAuditWorkers()
		}
	}
	return r, nil
}

func (r *Registry) Default() *Service { return r.services[r.defaultVersion] }
func (r *Registry) Lookup(version string) (*Service, bool) {
	if version == "" {
		version = r.defaultVersion
	}
	s, ok := r.services[version]
	return s, ok
}
func (r *Registry) Versions() []string {
	names := make([]string, 0, len(r.services))
	for name := range r.services {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
func (r *Registry) Close() {
	r.once.Do(func() {
		r.cancel()
		for _, s := range r.services {
			s.workers.Wait()
		}
	})
}

func (s *Service) createTables() error {
	for _, table := range []string{s.accountsTable(), s.teamsTable()} {
		columns := "uuid BINARY(16) PRIMARY KEY, data LONGTEXT NOT NULL"
		if table == s.teamsTable() {
			columns += ", version VARCHAR(32) NOT NULL"
		}
		if _, err := s.DB.Exec("CREATE TABLE IF NOT EXISTS " + table + " (" + columns + ", FOREIGN KEY (uuid) REFERENCES accounts(uuid) ON DELETE CASCADE)"); err != nil {
			return err
		}
	}
	_, err := s.DB.Exec(`CREATE TABLE IF NOT EXISTS ` + s.streaksTable() + ` (
		id VARCHAR(64) PRIMARY KEY, uuid BINARY(16) NOT NULL, runId VARCHAR(64) NOT NULL,
		wins INT NOT NULL DEFAULT 0, endedAt BIGINT NOT NULL DEFAULT 0, data LONGTEXT NOT NULL,
		UNIQUE KEY oneAttemptPerRun(uuid,runId), KEY finalRanking(endedAt,wins),
		FOREIGN KEY (uuid) REFERENCES accounts(uuid) ON DELETE CASCADE)`)
	if err != nil {
		return err
	}
	_, err = s.DB.Exec(`CREATE TABLE IF NOT EXISTS pvpDefenseResults (
		matchId VARCHAR(64) PRIMARY KEY, uuid BINARY(16) NOT NULL, version VARCHAR(32) NOT NULL,
		runId VARCHAR(64) NOT NULL, result VARCHAR(8) NOT NULL,
		KEY teamResults(uuid,version,runId), FOREIGN KEY (uuid) REFERENCES accounts(uuid) ON DELETE CASCADE)`)
	if err != nil {
		return err
	}
	_, err = s.DB.Exec(`CREATE TABLE IF NOT EXISTS pvpRegisteredParties (
		uuid BINARY(16) NOT NULL, version VARCHAR(32) NOT NULL, runId VARCHAR(64) NOT NULL,
		data LONGTEXT NOT NULL, registeredAt BIGINT NOT NULL,
		PRIMARY KEY(uuid,version,runId), KEY versionParties(version),
		FOREIGN KEY (uuid) REFERENCES accounts(uuid) ON DELETE CASCADE)`)
	return err
}

func loadRegistrySecret() ([]byte, error) {
	dir := os.Getenv("PVP_DATA_DIR")
	if dir == "" {
		dir = "/data"
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "pvp.key")
	secret, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		secret = make([]byte, 32)
		if _, err = rand.Read(secret); err != nil {
			return nil, err
		}
		err = os.WriteFile(path, secret, 0600)
	}
	if err != nil {
		return nil, err
	}
	if len(secret) != 32 {
		return nil, fmt.Errorf("invalid PvP secret length")
	}
	return secret, nil
}
