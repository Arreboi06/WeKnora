package repository

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"strings"

	"gorm.io/gorm"
)

const citationProfileUniverseLockNamespace = "weknora:citation-profile:source-universe:v1"

func lockCitationProfileSourceUniverseExclusive(db *gorm.DB, tenantID uint64, knowledgeBaseID string) error {
	return lockCitationProfileSourceUniverse(db, tenantID, knowledgeBaseID, false)
}

func lockCitationProfileSourceUniverseShared(db *gorm.DB, tenantID uint64, knowledgeBaseID string) error {
	return lockCitationProfileSourceUniverse(db, tenantID, knowledgeBaseID, true)
}

func lockCitationProfileSourceUniverse(
	db *gorm.DB,
	tenantID uint64,
	knowledgeBaseID string,
	shared bool,
) error {
	if db == nil {
		return errors.New("citation profile source-universe lock requires database")
	}
	if db.Dialector.Name() != "postgres" {
		return nil
	}

	lockFunction := "pg_advisory_xact_lock"
	if shared {
		lockFunction = "pg_advisory_xact_lock_shared"
	}
	return db.Exec("SELECT "+lockFunction+"(?)", citationProfileSourceUniverseLockKey(tenantID, knowledgeBaseID)).Error
}

func citationProfileSourceUniverseLockKey(tenantID uint64, knowledgeBaseID string) int64 {
	digest := sha256.New()
	_, _ = digest.Write([]byte(citationProfileUniverseLockNamespace))
	_, _ = digest.Write([]byte{0})
	var tenantBytes [8]byte
	binary.BigEndian.PutUint64(tenantBytes[:], tenantID)
	_, _ = digest.Write(tenantBytes[:])
	_, _ = digest.Write([]byte{0})
	_, _ = digest.Write([]byte(strings.TrimSpace(knowledgeBaseID)))
	sum := digest.Sum(nil)
	return int64(binary.BigEndian.Uint64(sum[:8]) & ((uint64(1) << 63) - 1))
}
