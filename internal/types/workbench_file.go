package types

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"time"
)

type WorkbenchFileRoot string

const (
	WorkbenchFileRootWorkspace WorkbenchFileRoot = "workspace"
	WorkbenchFileRootInput     WorkbenchFileRoot = "input"
	WorkbenchFileRootOutput    WorkbenchFileRoot = "output"
	WorkbenchFileRootArtifact  WorkbenchFileRoot = "artifact"
)

type WorkbenchFileRef struct {
	FileRefVersion int               `json:"file_ref_version"`
	Root           WorkbenchFileRoot `json:"root"`
	Segments       []string          `json:"segments"`
	ArtifactID     string            `json:"artifact_id,omitempty"`
	Version        int               `json:"version,omitempty"`
}

type WorkbenchFileEntry struct {
	Name      string           `json:"name"`
	Type      string           `json:"type"`
	SizeBytes int64            `json:"size_bytes"`
	ModTime   time.Time        `json:"mod_time"`
	Ref       WorkbenchFileRef `json:"ref"`
}

func (r WorkbenchFileRef) Value() (driver.Value, error) {
	return json.Marshal(r)
}

func (r *WorkbenchFileRef) Scan(src any) error {
	if src == nil {
		*r = WorkbenchFileRef{}
		return nil
	}
	var raw []byte
	switch v := src.(type) {
	case []byte:
		raw = v
	case string:
		raw = []byte(v)
	default:
		return errors.New("WorkbenchFileRef.Scan: unsupported source type")
	}
	if len(raw) == 0 {
		*r = WorkbenchFileRef{}
		return nil
	}
	return json.Unmarshal(raw, r)
}
