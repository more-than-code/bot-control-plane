package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
)

// ImageDeploy is one finished or in-flight image deploy.
// InFlight is not part of the HTTP object.
type ImageDeploy struct {
	ID         uuid.UUID  `json:"id"`
	EntryID    uuid.UUID  `json:"entry_id"`
	HostID     uuid.UUID  `json:"host_id"`
	Digest     *string    `json:"digest"`
	SpecHash   string     `json:"spec_hash"`
	Outcome    string     `json:"outcome"`
	ExitCode   *int       `json:"exit_code"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at"`
	By         *uuid.UUID `json:"by"`
	Via        string     `json:"via"`
	// EndsBy is set by the HTTP layer on the in-flight row of a history
	// list (api-v1 v1.42 item 1); null on every other row.
	EndsBy *time.Time `json:"ends_by"`
	// Reason says why a deploy, rollback or refusal did not succeed
	// (api-v1 v1.47). The HTTP layer fills a stored null from the outcome.
	Reason *string `json:"reason"`
	// FinalStatus is the container state the agent reported at the end;
	// null for rows without one (api-v1 v1.47).
	FinalStatus *DeployFinalStatus `json:"final_status"`
	// LogTail is the stored, redacted log tail. Only the single-deploy
	// route returns it, as lines.
	LogTail       []string  `json:"-"`
	InFlight      bool      `json:"-"`
	InFlightUntil time.Time `json:"-"`
}

// DeployTask is the deploy or rollback row an agent task id names (api-v1
// v1.50 item 3), with the action and the entry's project and name.
type DeployTask struct {
	Deploy    ImageDeploy
	Action    string
	ProjectID uuid.UUID
	Entry     string
}

// DeployFinalStatus is the part of ImageStatus a deploy row keeps.
type DeployFinalStatus struct {
	State        string `json:"state"`
	ExitCode     *int   `json:"exit_code"`
	RestartCount int    `json:"restart_count"`
}

// DeployFinish is what FinishImageDeployWith stores on an in-flight row.
type DeployFinish struct {
	Outcome     string
	Digest      *string
	ExitCode    *int
	Reason      *string
	FinalStatus *DeployFinalStatus
	LogTail     []string
	At          time.Time
}

// ImageOpRunning is BeginImageOp refused because another deploy, rollback
// or remove holds (host, entry) (api-v1 v1.42 item 2). StartedAt and Until
// are that row's started_at and in_flight_until. errors.Is(err, ErrConflict)
// holds.
type ImageOpRunning struct {
	StartedAt time.Time
	Until     time.Time
}

func (e *ImageOpRunning) Error() string { return "conflict" }

func (e *ImageOpRunning) Unwrap() error { return ErrConflict }

// AutoActionsPerHour is the rev 2 cap on stored auto_action rows per
// (entry, host) in a rolling hour.
const AutoActionsPerHour = 20

// AutoTarget is the entry an auto_action names, as one host sees it.
// Deployed is true when the entry has a succeeded or unchanged deploy row
// on that host.
type AutoTarget struct {
	ProjectID uuid.UUID
	EntryID   uuid.UUID
	ImageRaw  json.RawMessage
	Deployed  bool
}

// AutoAction is one checked agent auto_action to store (api-v1 v1.15 item 3).
// Outcome is auto_restarted, auto_rolled_back, auto_gave_up, or auto_failed.
// Digest is nil when the frame sent null or the outcome is auto_failed.
// At is server time.
type AutoAction struct {
	ID      uuid.UUID
	EntryID uuid.UUID
	HostID  uuid.UUID
	Outcome string
	Digest  *string
	At      time.Time
}

// IsAutoOutcome reports the automatic deploy-row outcomes.
func IsAutoOutcome(outcome string) bool {
	switch outcome {
	case "auto_restarted", "auto_rolled_back", "auto_gave_up", "auto_failed":
		return true
	}
	return false
}

// EnvKey is one env-set key in an HTTP body. Value is null when the key is secret.
// Preview is the v1.13 preview of a secret and null for a plain key.
// The store leaves Preview nil. The HTTP handler fills it and never puts the secret in Value.
// RevealPending is true from a generated write until its one reveal; NeedsValue
// marks a placeholder that has no value yet (api-v1 v1.27 item 2).
// Generated is true while the stored value is API-generated; it only picks
// the preview (always four bullets) and is not part of the HTTP object.
type EnvKey struct {
	Key           string  `json:"key"`
	IsSecret      bool    `json:"is_secret"`
	Value         *string `json:"value"`
	Preview       *string `json:"preview"`
	RevealPending bool    `json:"reveal_pending"`
	NeedsValue    bool    `json:"needs_value"`
	Generated     bool    `json:"-"`
	// Replaced is set by FillValueRequest when the fill replaced a stored
	// secret value (api-v1 v1.33 item 3). It is not JSON.
	Replaced bool `json:"-"`
}

// EnvKeyPut is one key for PutEnvKey.
// Secret nil stores a new key as secret and leaves an existing flag unchanged.
// Nonce and Ciphertext seal Plain when the stored flag is secret.
// Generated marks an API-generated value: a secret key is stored with
// reveal_pending and generated true. Any other write stores both false.
type EnvKeyPut struct {
	Key        string
	Plain      string
	Secret     *bool
	Nonce      []byte
	Ciphertext []byte
	KeyVersion int
	Generated  bool
}

// EnvSet is one project env set.
type EnvSet struct {
	Name      string    `json:"name"`
	Keys      []EnvKey  `json:"keys"`
	UpdatedAt time.Time `json:"updated_at"`
	UpdatedBy uuid.UUID `json:"updated_by"`
	// Entry is the owning entry's name for a private set "@<entry>"
	// (api-v1 v1.33 item 6), empty for a shared set.
	Entry string `json:"entry,omitempty"`
	// UsedBy is set by the env-set reads only (api-v1 v1.33 item 4): the
	// entries naming the set in env_sets or secret_env, sorted.
	UsedBy *[]string `json:"used_by,omitempty"`
}

// EnvSetChange is one confirmed env-set plan (api-v1 v1.33 items 1 and 2):
// delete the set, or set plain values, add keys, remove keys and make plain
// keys secret, all in one transaction. Set values are plain and never secret.
type EnvSetChange struct {
	// StateHash, when set, is EnvStateHash of the set when the plan was
	// made; a set that differs under the lock is ErrEnvSetChanged.
	StateHash  string
	Delete     bool
	Set        map[string]string
	Add        []EnvKeyCreate
	Remove     []string
	MakeSecret []string
}

// PrivateEnv is the private set write of an image or program entry write
// (api-v1 v1.33 item 6). Keys is the final key list of "@<entry>": keys of
// the set not in it are removed, and the set is deleted when it is empty.
// Add holds the new keys (generated or secret placeholders), already sealed.
type PrivateEnv struct {
	Keys []string
	Add  []EnvKeyCreate
}

// EnvSecret is one env value prepared for PutEnvSet, CreateEnvSet, and MergeEnvSet.
// Nonce, Ciphertext, and KeyVersion seal Plain (AAD project_id|set|key).
// Plain is always set, also for a secret key: the stores refuse "" over an
// existing secret key (ErrSecretEmpty) by reading it.
// The store keeps Plain for a key that is plain (an existing plain key, or a
// new key when newKeysSecret is false) and the seal for a key that is secret.
type EnvSecret struct {
	Key        string
	Plain      string
	Nonce      []byte
	Ciphertext []byte
	KeyVersion int
}

// EnvSealer seals one stored plain value for MakeEnvKeysSecret, which calls
// it while holding the set's locks. It must be pure CPU work and must not
// call the Store. Only Nonce, Ciphertext, and KeyVersion of its result are read.
type EnvSealer func(key, plain string) (EnvSecret, error)

// MissingEnvKeyError is a MakeEnvKeysSecret key that is not in the set.
// It matches ErrNotFound. The message names the key, never a value.
type MissingEnvKeyError struct {
	Key string
}

func (e *MissingEnvKeyError) Error() string { return "key " + e.Key + " not found" }

func (e *MissingEnvKeyError) Is(target error) bool { return target == ErrNotFound }

// EmptySecretKeyError is a MakeEnvKeysSecret key whose plain value is empty
// and that is not a needs_value placeholder (spec-release-v0239 F6): it
// would become an empty secret, so nothing changes. It matches
// ErrSecretEmpty. The message names the key.
type EmptySecretKeyError struct {
	Key string
}

func (e *EmptySecretKeyError) Error() string {
	return "a secret value cannot be empty; give " + e.Key + " a value first"
}

func (e *EmptySecretKeyError) Is(target error) bool { return target == ErrSecretEmpty }

// EnvMaterial is a stored env value, including ciphertext. It is not returned over HTTP.
// Generated is the v1.27 generated flag (see EnvKey).
type EnvMaterial struct {
	Key        string
	IsSecret   bool
	Plain      string
	Nonce      []byte
	Ciphertext []byte
	KeyVersion int
	Generated  bool
	// NeedsValue marks a placeholder with no value yet (v1.27 item 4). A
	// deploy or rollback that resolves it is refused (item 6).
	NeedsValue bool
}

// EnvKeyCreate is one key of CreateEnvSetNames (api-v1 v1.27 item 4). It
// never carries a typed secret value; a plain key may carry Value (v1.32
// item 2). A Generated key is a sealed API-generated secret. Any other key
// without Value is a placeholder: an empty value with needs_value,
// secret when Secret is true (Nonce and Ciphertext then seal "").
type EnvKeyCreate struct {
	Key       string
	Secret    bool
	Generated bool
	// Value is a plain key's value (api-v1 v1.32 item 2). It is set only
	// when Secret and Generated are false; nil leaves a placeholder.
	Value      *string
	Nonce      []byte
	Ciphertext []byte
	KeyVersion int
}

// EnvSetConfirmation is a single-use delegated env set create (api-v1 v1.27
// item 4), stored by token hash in app.env_set_confirmations. Body is the
// normalized request keys (names, generate objects, is_secret, and a plain
// key's value, api-v1 v1.32 item 2); it never holds a secret value. RequestHash binds the request without the idempotency key.
type EnvSetConfirmation struct {
	UserID         uuid.UUID
	ProjectID      uuid.UUID
	SetName        string
	Body           json.RawMessage
	RequestHash    []byte
	IdempotencyKey string
	CreatedAt      time.Time
	ExpiresAt      time.Time
	UsedAt         *time.Time
}

// Registry sources (api-v1 v1.29 item 5).
const (
	RegistrySourceManual = "manual"
	RegistrySourceGitHub = "github"
)

// GitHubRegistry is the registry host a GitHub connection stores (ghcr.io).
const GitHubRegistry = "ghcr.io"

// Registry is a registry credential without the token.
type Registry struct {
	Host      string    `json:"host"`
	Username  string    `json:"username"`
	UpdatedAt time.Time `json:"updated_at"`
	UpdatedBy uuid.UUID `json:"updated_by"`
	// Source is manual or github (api-v1 v1.29 item 5).
	Source string `json:"source"`
	// ConnectedBy is the user who connected GitHub; nil for a manual row,
	// and nil when that user no longer exists.
	ConnectedBy *RegistryUser `json:"connected_by"`
	// DeletePackages is true when GitHub granted delete:packages on the
	// row's last connect (api-v1 v1.45, migration 0030); false for a manual row.
	DeletePackages bool `json:"delete_packages"`
}

// RegistryUser names the user who connected a registry.
type RegistryUser struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
}

// RegistryWrite is an encrypted registry token. An empty Source is manual.
// ConnectedBy and the refresh material are stored only for a github row.
type RegistryWrite struct {
	Username    string
	Nonce       []byte
	Ciphertext  []byte
	KeyVersion  int
	Source      string
	ConnectedBy *uuid.UUID
	// DeletePackages is stored only for a github row (api-v1 v1.45).
	DeletePackages bool
	RegistryRefreshMaterial
}

// RegistryRefreshMaterial is the expiring GitHub token data of a github row
// (api-v1 v1.43, migration 0028): the sealed refresh token (AAD
// seal.RegistryRefreshAAD) and both expiry times. Empty or nil when GitHub
// issued a long-lived token, for a manual row, and after a rejected refresh
// (AccessExpiresAt then stays).
type RegistryRefreshMaterial struct {
	RefreshNonce      []byte
	RefreshCiphertext []byte
	AccessExpiresAt   *time.Time
	RefreshExpiresAt  *time.Time
}

// HasRefresh reports whether a refresh token is stored.
func (m RegistryRefreshMaterial) HasRefresh() bool {
	return len(m.RefreshNonce) > 0 && len(m.RefreshCiphertext) > 0
}

// RegistryMaterial is one stored registry token. It is not returned over HTTP.
type RegistryMaterial struct {
	Host       string
	Username   string
	Nonce      []byte
	Ciphertext []byte
	KeyVersion int
	Source     string
	// DeletePackages: GitHub granted delete:packages (api-v1 v1.45).
	DeletePackages bool
	RegistryRefreshMaterial
}

// RegistryRefresh is a refreshed GitHub token for RefreshRegistry: the new
// sealed access token and the new refresh material.
type RegistryRefresh struct {
	Nonce      []byte
	Ciphertext []byte
	KeyVersion int
	RegistryRefreshMaterial
}

// Norm fills in the manual source and drops ConnectedBy and the refresh
// material for a manual row. A refresh nonce without a ciphertext (or the
// reverse) is dropped.
func (in RegistryWrite) Norm() RegistryWrite {
	if in.Source == "" {
		in.Source = RegistrySourceManual
	}
	if in.Source != RegistrySourceGitHub {
		in.ConnectedBy = nil
		in.DeletePackages = false
		in.RegistryRefreshMaterial = RegistryRefreshMaterial{}
	}
	if !in.HasRefresh() {
		in.RefreshNonce, in.RefreshCiphertext, in.RefreshExpiresAt = nil, nil, nil
	}
	return in
}

// DefaultGrantActions is the grant inserted for a new entry.
// Program owners do not receive deploy. Image owners and admins do.
func DefaultGrantActions(kind string) (admin, viewer []string) {
	if kind == "image" {
		return []string{"run", "stop", "restart", "logs", "deploy"}, []string{"logs"}
	}
	return []string{"run", "stop", "restart", "logs"}, []string{"logs"}
}

// ErrEnvSetChanged is a confirmed env set plan whose set no longer matches
// the state it was planned on (api-v1 v1.33 items 1 and 2).
var ErrEnvSetChanged = errors.New("env set changed since the plan")

// EnvStateHash is a digest of an env set's state as a plan sees it: each
// key in byte order with is_secret, needs_value, and the plain value or the
// ciphertext of a secret. reveal_pending and generated are left out, so a
// reveal does not change it; every value or key write does. It holds no
// value in clear and is stored only in a plan body.
func EnvStateHash(mats []EnvMaterial) string {
	rows := append([]EnvMaterial(nil), mats...)
	sort.Slice(rows, func(i, j int) bool { return rows[i].Key < rows[j].Key })
	h := sha256.New()
	for _, m := range rows {
		fmt.Fprintf(h, "%s\x00%t\x00%t\x00", m.Key, m.IsSecret, m.NeedsValue)
		if m.IsSecret {
			h.Write([]byte(hex.EncodeToString(m.Ciphertext)))
		} else {
			h.Write([]byte(m.Plain))
		}
		h.Write([]byte{0xff})
	}
	return hex.EncodeToString(h.Sum(nil))
}
