package store

import (
	"context"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

// Host config files (api-v1 v1.44, migration 0029, spec
// docs/spec-host-config-files.md §2.7).

const (
	// HostFileKeep is how many versions per file a save keeps (the newest),
	// besides the applied one.
	HostFileKeep = 50
	// MaxHostFileSize is the largest file content, in bytes.
	MaxHostFileSize = 65536
	// MaxHostFileVersion is the largest version number a request may name.
	MaxHostFileVersion = 1 << 30
	// HostFilesStatusStale: a stored status older than this reads as
	// unknown (the agent reports every 10 minutes).
	HostFilesStatusStale = 25 * time.Minute
	// MaxHostFiles is how many files one host may declare (agent-protocol
	// v1.18 item 1).
	MaxHostFiles = 8
)

// Host file apply outcomes (agent-protocol v1.18 item 4).
const (
	HostFileSucceeded      = "succeeded"
	HostFileUnchanged      = "unchanged"
	HostFileWorkerBusy     = "worker_busy"
	HostFileCheckFailed    = "check_failed"
	HostFileRolledBack     = "rolled_back"
	HostFileRollbackFailed = "rollback_failed"
	HostFileForbiddenSpec  = "forbidden_spec"
)

// ValidHostFileOutcome reports whether o is an agent apply outcome.
func ValidHostFileOutcome(o string) bool {
	switch o {
	case HostFileSucceeded, HostFileUnchanged, HostFileWorkerBusy, HostFileCheckFailed,
		HostFileRolledBack, HostFileRollbackFailed, HostFileForbiddenSpec:
		return true
	}
	return false
}

// Host file status words (api-v1 v1.44 item 2).
const (
	HostFileInSync     = "in_sync"
	HostFileDifferent  = "different"
	HostFileMissing    = "missing"
	HostFileWrongOwner = "wrong_owner"
	HostFileUnknown    = "unknown"
)

var hostFileNameRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

// ValidHostFileName is the runner.toml host file name rule.
func ValidHostFileName(name string) bool { return hostFileNameRe.MatchString(name) }

// HostFile is one hello.programs.host_files entry (agent-protocol v1.18
// item 3). No content and no command lines.
type HostFile struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	Secret bool   `json:"secret"`
	Unit   string `json:"unit"`
	Health bool   `json:"health"`
}

// HostFilesStatus is the latest host_files_status frame of a host, stored
// in app.hosts.host_files_status with the time it arrived.
type HostFilesStatus struct {
	At    time.Time        `json:"at"`
	Files []HostFileStatus `json:"files"`
}

// HostFileStatus is one file of the frame. Size and SHA256 are only ever
// set for a file that is not secret. BadSince is set by the API: since
// when the status word has been different, missing or wrong_owner.
type HostFileStatus struct {
	Name           string     `json:"name"`
	Exists         bool       `json:"exists"`
	OwnerOK        bool       `json:"owner_ok"`
	Reason         string     `json:"reason,omitempty"`
	MatchesApplied *bool      `json:"matches_applied"`
	Size           *int       `json:"size,omitempty"`
	SHA256         string     `json:"sha256,omitempty"`
	BadSince       *time.Time `json:"bad_since,omitempty"`
}

// Status reasons the agent may report (spec amendment A5). Others are dropped.
var hostFileStatusReasons = map[string]string{
	"missing":     HostFileMissing,
	"symlink":     HostFileWrongOwner,
	"not_regular": HostFileWrongOwner,
	"owner":       HostFileWrongOwner,
	"mode":        HostFileWrongOwner,
	"nlink":       HostFileWrongOwner,
	"dir":         HostFileWrongOwner,
	"too_large":   HostFileUnknown,
	"io_error":    HostFileUnknown,
}

// ValidHostFileStatusReason reports whether r is a status reason token.
func ValidHostFileStatusReason(r string) bool {
	_, ok := hostFileStatusReasons[r]
	return ok
}

// Word is the api-v1 v1.44 status of one reported file. A reason decides
// first: missing is missing; symlink, not_regular, owner, mode, nlink and
// dir are wrong_owner; too_large and io_error are unknown (the file could
// not be read). Then the flags: not exists is missing, not owner_ok is
// wrong_owner, and matches_applied gives in_sync or different, unknown
// while nothing was applied (null).
func (f HostFileStatus) Word() string {
	if word, ok := hostFileStatusReasons[f.Reason]; ok {
		return word
	}
	switch {
	case !f.Exists:
		return HostFileMissing
	case !f.OwnerOK:
		return HostFileWrongOwner
	case f.MatchesApplied == nil:
		return HostFileUnknown
	case *f.MatchesApplied:
		return HostFileInSync
	default:
		return HostFileDifferent
	}
}

// FileWord is the status word of the named file in a stored status at
// now: unknown when there is no status, it is older than
// HostFilesStatusStale, or it does not report the file.
func FileWord(status *HostFilesStatus, name string, now time.Time) string {
	if status == nil || now.Sub(status.At) > HostFilesStatusStale {
		return HostFileUnknown
	}
	for _, f := range status.Files {
		if f.Name == name {
			return f.Word()
		}
	}
	return HostFileUnknown
}

// Drifted reports whether the word is one the bell watches.
func Drifted(word string) bool {
	return word == HostFileDifferent || word == HostFileMissing || word == HostFileWrongOwner
}

// HostFileVersion is one saved version. Plain is the content of a plain
// file when read with content; Nonce, Ciphertext and KeyVersion hold a
// secret file's sealed content. SHA256 is empty for a secret file.
type HostFileVersion struct {
	HostID     uuid.UUID
	Name       string
	Version    int
	Secret     bool
	Size       int
	SHA256     string
	Plain      string
	Nonce      []byte
	Ciphertext []byte
	KeyVersion int
	CreatedBy  uuid.UUID
	CreatedAt  time.Time
}

// HostFileSave is a new version. BaseVersion, when set, must equal the
// latest version (0 for none) or the save is ErrConflict. For a secret
// file Seal is called with the new version number, under the save's lock,
// and returns the sealed content (the AAD binds the version).
type HostFileSave struct {
	HostID      uuid.UUID
	Name        string
	Secret      bool
	Size        int
	SHA256      string
	Plain       string
	Seal        func(version int) (nonce, ciphertext []byte, keyVersion int, err error)
	BaseVersion *int
	// Protect, when set, returns a version the prune must keep besides the
	// applied one: the target of an apply in flight (0 for none). The store
	// calls it under the save's per-file lock, which GetHostFileVersion also
	// takes, so an apply that starts during the save either is seen here or
	// reads the version only after the save committed.
	Protect   func() int
	CreatedBy uuid.UUID
	At        time.Time
}

// HostFileApply is one apply result.
type HostFileApply struct {
	HostID  uuid.UUID
	Name    string
	Version int
	Outcome string
	Stage   string
	Reason  string
	TookMS  int
	By      uuid.UUID
	At      time.Time
}

// HostFileState is one name's summary on a host: the latest saved
// version, the applied one (the last apply that succeeded or was
// unchanged) and the last apply, each nil when there is none.
type HostFileState struct {
	Name           string
	LatestVersion  *int
	AppliedVersion *int
	LastApply      *HostFileApply
}

// HostFilesStatusRow is one live host with a stored status (the sweep).
type HostFilesStatusRow struct {
	HostID uuid.UUID
	OrgID  uuid.UUID
	Name   string
	Status HostFilesStatus
}

// ValidHostFileSave checks the stored form: name, size 1 to
// MaxHostFileSize, a plain file with content of that size and a sha256, a
// secret file with a Seal and no plain content or hash.
func ValidHostFileSave(in HostFileSave) bool {
	if in.HostID == uuid.Nil || !ValidHostFileName(in.Name) || in.Size < 1 || in.Size > MaxHostFileSize {
		return false
	}
	if in.BaseVersion != nil && *in.BaseVersion < 0 {
		return false
	}
	if in.Secret {
		return in.Seal != nil && in.Plain == "" && in.SHA256 == ""
	}
	return len(in.Plain) == in.Size && !strings.ContainsRune(in.Plain, 0) && utf8.ValidString(in.Plain) &&
		sha256Re.MatchString(in.SHA256) && in.Seal == nil
}

var sha256Re = regexp.MustCompile(`^[0-9a-f]{64}$`)

// HostFileStore keeps host file versions, applies and the agent's status.
type HostFileStore interface {
	// SaveHostFileVersion stores the next version (latest + 1) and deletes
	// versions beyond the newest HostFileKeep, never the applied one or
	// in.Protect(). A
	// stale BaseVersion is ErrConflict; an invalid input ErrInvalid.
	SaveHostFileVersion(ctx context.Context, in HostFileSave) (int, error)
	// ListHostFileVersions returns the versions of one file without content,
	// newest first.
	ListHostFileVersions(ctx context.Context, hostID uuid.UUID, name string) ([]HostFileVersion, error)
	// GetHostFileVersion returns one version with its content; version 0 is
	// the latest. None is ErrNotFound. It waits for a save of that file in
	// progress (the same per-file lock).
	GetHostFileVersion(ctx context.Context, hostID uuid.UUID, name string, version int) (HostFileVersion, error)
	// RecordHostFileApply appends an apply result.
	RecordHostFileApply(ctx context.Context, in HostFileApply) error
	// HostFileStates returns the summary of every name with a version or an
	// apply on the host, by name.
	HostFileStates(ctx context.Context, hostID uuid.UUID) ([]HostFileState, error)
	// GetHostFilesStatus returns the stored status, nil when none.
	GetHostFilesStatus(ctx context.Context, hostID uuid.UUID) (*HostFilesStatus, error)
	// SetHostFilesStatus replaces the stored status. A missing host is ErrNotFound.
	SetHostFilesStatus(ctx context.Context, hostID uuid.UUID, status HostFilesStatus) error
	// ListHostFilesStatuses returns every host that is not revoked and has
	// a stored status, by host id.
	ListHostFilesStatuses(ctx context.Context) ([]HostFilesStatusRow, error)
}
