package store

import (
	"encoding/json"

	"github.com/more-than-code/bot-control-plane/api/internal/catalog"
	"reflect"
	"regexp"
	"time"

	"github.com/google/uuid"
)

// serviceNameRe is the agent service name rule. A fallback unit name that
// fails it is not stored as the service name.
var serviceNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)

// IdempotencyTTL is how long a run, stop, or restart response is replayed.
const IdempotencyTTL = 24 * time.Hour

// ConfirmationTTL is how long a delegated confirmation stays usable.
const ConfirmationTTL = 5 * time.Minute

// HostPrograms is the agent hello programs object. A nil pointer on Host
// means the agent reported no policy file.
type HostPrograms struct {
	Enabled      bool     `json:"enabled"`
	RunAs        []string `json:"run_as"`
	AllowShell   bool     `json:"allow_shell"`
	PolicyHash   string   `json:"policy_hash"`
	Images       bool     `json:"images"`
	Docker       bool     `json:"docker"`
	Files        bool     `json:"files"`
	DockerHealth bool     `json:"docker_health"`
	Healthcheck  bool     `json:"healthcheck"`
	Logging      bool     `json:"logging"`
	HealthPolicy bool     `json:"health_policy"`
	// Secrets is hello.programs.secrets (api-v1 v1.22 item 5); absent = false.
	Secrets bool `json:"secrets"`
	// Projects is hello.programs.projects (api-v1 v1.23 item 1): the host's
	// runner.toml allowlist. Nil (JSON null) when the agent did not send it.
	// The API returns it only to org owners and admins (api-v1 v1.24 item 1).
	Projects []string `json:"projects"`
	// ProjectListed is computed by the API for the viewing project (api-v1
	// v1.23 item 1). It is never read from hello and nil in the store.
	ProjectListed *bool `json:"project_listed"`
	// SharedNetworks is hello.programs.shared_networks (api-v1 v1.25 item 2).
	// Nil (JSON null) when the agent did not send it (agents < 0.2.9).
	SharedNetworks []SharedNetwork `json:"shared_networks"`
	// EnvHash is hello.programs.env_hash (api-v1 v1.27 item 8, agent-protocol
	// v1.14 item 1); absent = false.
	EnvHash bool `json:"env_hash"`
	// Edge is hello.programs.edge (api-v1 v1.35 item 2, agent-protocol v1.15
	// item 2). Nil when the agent did not send it (agents < 0.2.11): the
	// edge is then not available.
	Edge *HostEdgePolicy `json:"edge,omitempty"`
	// HostFiles is hello.programs.host_files (api-v1 v1.44, agent-protocol
	// v1.18 item 3). Nil when the agent did not send it (agents before
	// v1.18): host file applies are then unsupported. The API never puts it
	// on a Host answer; the files route lists it.
	HostFiles []HostFile `json:"host_files,omitzero"`
	// AgentUpgrade is hello.programs.agent_upgrade (agent-protocol v1.20
	// item 2); absent = false. The API never puts it on a Host answer; the
	// Host carries agent_upgrade_allowed instead (api-v1 v1.48 item 4).
	AgentUpgrade bool `json:"agent_upgrade,omitempty"`
	// HostNetwork is hello.programs.host_network (agent-protocol v1.21 item
	// 2); absent = false. It gates ImageSpec network "host" (api-v1 v1.50
	// item 1). The API never puts it on a Host answer.
	HostNetwork bool `json:"host_network,omitempty"`
}

// HostEdgePolicy is the runner.toml edge switch and the Caddy image digest
// the agent release pins.
type HostEdgePolicy struct {
	Enabled bool   `json:"enabled"`
	Image   string `json:"image"`
}

// EdgeAvailable reports whether the host's policy allows the edge.
func (p *HostPrograms) EdgeAvailable() bool {
	return p != nil && p.Edge != nil && p.Edge.Enabled
}

// SharedNetwork is one runner.toml shared network (agent-protocol v1.13).
// Projects is the member slugs, never nil once parsed from hello. The API
// sets it to nil for callers who are not org admins (api-v1 v1.25 item 2);
// omitzero leaves a nil Projects out of the JSON, and an empty list stays [].
type SharedNetwork struct {
	Name     string   `json:"name"`
	Subnet   string   `json:"subnet"`
	Projects []string `json:"projects,omitzero"`
}

// CloneSharedNetworks is a deep copy. nil stays nil, and a nil Projects
// stays nil.
func CloneSharedNetworks(nets []SharedNetwork) []SharedNetwork {
	if nets == nil {
		return nil
	}
	out := make([]SharedNetwork, len(nets))
	for i, n := range nets {
		out[i] = n
		if n.Projects != nil {
			out[i].Projects = append([]string{}, n.Projects...)
		}
	}
	return out
}

// ProgramSpec is the stored program document. Env, Args, EnvSets and
// SecretEnv are never nil after a write or a read. EnvSets and SecretEnv are
// api-v1 v1.22 item 1; a document stored before v1.22 reads as empty.
type ProgramSpec struct {
	Dir       string            `json:"dir"`
	Exec      string            `json:"exec"`
	Args      []string          `json:"args"`
	Shell     bool              `json:"shell"`
	RunAs     string            `json:"run_as"`
	Env       map[string]string `json:"env"`
	EnvSets   []string          `json:"env_sets"`
	SecretEnv map[string]string `json:"secret_env"`
	Port      *int              `json:"port"`
	Restart   string            `json:"restart"`
}

// Norm makes Args, Env, EnvSets and SecretEnv non-nil.
func (s *ProgramSpec) Norm() {
	if s.Args == nil {
		s.Args = []string{}
	}
	if s.Env == nil {
		s.Env = map[string]string{}
	}
	if s.EnvSets == nil {
		s.EnvSets = []string{}
	}
	if s.SecretEnv == nil {
		s.SecretEnv = map[string]string{}
	}
}

// Program is the catalog form of the stored spec, for spec_hash and env
// resolution. Display is not stored and stays empty.
func (s ProgramSpec) Program() catalog.Program {
	return catalog.Program{
		Dir: s.Dir, Exec: s.Exec, Args: s.Args, Shell: s.Shell, RunAs: s.RunAs,
		Env: s.Env, EnvSets: s.EnvSets, SecretEnv: s.SecretEnv, Port: s.Port, Restart: s.Restart,
	}
}

// CatalogEntry is one program the project may run. MyActions is filled by
// the API for the caller; the store leaves it nil. LastDeployedAt (api-v1
// v1.21) is filled by the API on the list and single-entry reads from
// CatalogLastDeployed; it is nil (JSON null) everywhere else.
type CatalogEntry struct {
	ID             uuid.UUID       `json:"id"`
	ProjectID      uuid.UUID       `json:"project_id"`
	Name           string          `json:"name"`
	Kind           string          `json:"kind"`
	Spec           ProgramSpec     `json:"spec"`
	ImageRaw       json.RawMessage `json:"-"`
	SpecHash       string          `json:"spec_hash"`
	CommandDisplay string          `json:"command_display"`
	CreatedBy      uuid.UUID       `json:"created_by"`
	CreatedAt      time.Time       `json:"created_at"`
	UpdatedAt      time.Time       `json:"updated_at"`
	MyActions      []string        `json:"my_actions"`
	LastDeployedAt *time.Time      `json:"last_deployed_at"`
}

// CatalogWrite is a new or replaced catalog entry.
// Kind is program or image. Empty means program.
// ImageRaw is the ImageSpec JSON when Kind is image.
type CatalogWrite struct {
	Name           string
	Kind           string
	Spec           ProgramSpec
	ImageRaw       json.RawMessage
	SpecHash       string
	CommandDisplay string
	Actor          uuid.UUID
	At             time.Time
	// Private is the entry's private set write (api-v1 v1.33 item 6). nil
	// leaves the private set as it is.
	Private *PrivateEnv
	// PlanBody, when set, is what CommitCatalogWrite compares with the stored
	// plan body instead of ImageRaw: a plan with new private keys stores them
	// in their object form.
	PlanBody json.RawMessage
}

// MarshalJSON returns a program entry with its ProgramSpec (the v1.6 fields
// plus env_sets and secret_env from api-v1 v1.22). An image entry returns its
// stored ImageSpec instead of the program spec.
func (e CatalogEntry) MarshalJSON() ([]byte, error) {
	if e.Kind != "image" {
		type alias CatalogEntry
		return json.Marshal(alias(e))
	}
	spec := e.ImageRaw
	if len(spec) == 0 {
		spec = json.RawMessage("null")
	} else {
		spec = ensureFiles(spec)
	}
	return json.Marshal(struct {
		ID             uuid.UUID       `json:"id"`
		ProjectID      uuid.UUID       `json:"project_id"`
		Name           string          `json:"name"`
		Kind           string          `json:"kind"`
		Spec           json.RawMessage `json:"spec"`
		SpecHash       string          `json:"spec_hash"`
		CommandDisplay string          `json:"command_display"`
		CreatedBy      uuid.UUID       `json:"created_by"`
		CreatedAt      time.Time       `json:"created_at"`
		UpdatedAt      time.Time       `json:"updated_at"`
		MyActions      []string        `json:"my_actions"`
		LastDeployedAt *time.Time      `json:"last_deployed_at"`
	}{
		ID: e.ID, ProjectID: e.ProjectID, Name: e.Name, Kind: e.Kind, Spec: spec,
		SpecHash: e.SpecHash, CommandDisplay: e.CommandDisplay, CreatedBy: e.CreatedBy,
		CreatedAt: e.CreatedAt, UpdatedAt: e.UpdatedAt, MyActions: e.MyActions,
		LastDeployedAt: e.LastDeployedAt,
	})
}

// ensureFiles adds "files":[] when a stored image spec omitted the key.
// A spec that already has the key is returned unchanged.
func ensureFiles(raw json.RawMessage) json.RawMessage {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return raw
	}
	if _, ok := fields["files"]; ok {
		return raw
	}
	fields["files"] = json.RawMessage(`[]`)
	out, err := json.Marshal(fields)
	if err != nil {
		return raw
	}
	return out
}

// CatalogGrant is one subject allowed to act on an entry.
// Subject is role:admin, role:viewer, or user:<uuid>.
type CatalogGrant struct {
	Subject string   `json:"subject"`
	Actions []string `json:"actions"`
}

// CatalogConfirmation is a single-use delegated action, stored by token hash.
type CatalogConfirmation struct {
	UserID         uuid.UUID
	ProjectID      uuid.UUID
	EntryID        uuid.UUID
	EntryName      string
	HostID         uuid.UUID
	Action         string
	SpecHash       string
	IdempotencyKey string
	RequestHash    []byte
	CreatedAt      time.Time
	ExpiresAt      time.Time
	UsedAt         *time.Time
	// ImageDigest and ImageForce rebuild a confirmed rollback or deploy.
	// They are nil for program actions. They are not secrets.
	ImageDigest *string
	ImageForce  *bool
}

// ServiceConfirmation is a single-use delegated health-check replacement.
// It is stored by token hash in app.service_confirmations, separate from
// catalog confirmations. Checks is the normalized set to apply. RequestHash
// binds the request. CurrentHash binds the check set at issue time.
type ServiceConfirmation struct {
	UserID uuid.UUID
	// ProjectID is the service's project; nil for an org-level service (api-v1 v1.24).
	ProjectID      *uuid.UUID
	ServiceID      uuid.UUID
	Action         string
	RequestHash    []byte
	CurrentHash    []byte
	Checks         []HealthCheckInput
	IdempotencyKey string
	CreatedAt      time.Time
	ExpiresAt      time.Time
	UsedAt         *time.Time
}

// CatalogWriteConfirmation is a single-use delegated catalog create, update,
// or delete. It is stored by token hash in app.catalog_write_confirmations.
// Body is the normalized ImageSpec for create and update, and nil for delete.
// It is never returned on the 202. BaseSpecHash is empty for create; the
// store writes that empty string, and a nil Body, as SQL NULL. RequestHash
// binds the request. It does not include the idempotency key.
type CatalogWriteConfirmation struct {
	UserID         uuid.UUID
	ProjectID      uuid.UUID
	Action         string
	EntryName      string
	BaseSpecHash   string
	Body           json.RawMessage
	RequestHash    []byte
	IdempotencyKey string
	CreatedAt      time.Time
	ExpiresAt      time.Time
	UsedAt         *time.Time
}

// SameJSON reports whether a and b are the same JSON value.
// Object key order does not matter. A nil slice and a JSON null are not equal.
func SameJSON(a, b []byte) bool {
	if len(a) == 0 || len(b) == 0 {
		return len(a) == 0 && len(b) == 0
	}
	var left, right any
	if json.Unmarshal(a, &left) != nil || json.Unmarshal(b, &right) != nil {
		return false
	}
	return reflect.DeepEqual(left, right)
}

// IdempotencyRecord is one stored run, stop, or restart response.
// Body is the full JSON envelope. Pending means the response is not stored yet.
type IdempotencyRecord struct {
	RequestHash []byte
	Status      int
	Body        []byte
	CreatedAt   time.Time
	Pending     bool
}

// DiscoveredOwner is the agent-protocol v1.3 owner object.
type DiscoveredOwner struct {
	Kind    string
	Project string
	Entry   string
}

// PlaceDiscovered decides the service row for one reported item.
// lookup reports the project id when that slug names a live project in the
// host's org with deploy access to the host and a live catalog entry of the
// given name and kind (api-v1 v1.24 item 6). A matching owner is stored as
// source owned. Anything else is an org-level discovered row: project nil.
// A program owner that misses falls back to the unit name. An image owner
// that misses keeps the reported container name.
func PlaceDiscovered(item Discovered, lookup func(slug, entry, kind string) (uuid.UUID, bool)) (name, source string, projectID *uuid.UUID, catalog *string) {
	if item.Owner == nil || item.Owner.Project == "" || item.Owner.Entry == "" {
		return item.Name, "discovered", nil, nil
	}
	if item.Owner.Kind != "program" && item.Owner.Kind != "image" {
		return item.Name, "discovered", nil, nil
	}
	if id, ok := lookup(item.Owner.Project, item.Owner.Entry, item.Owner.Kind); ok {
		ce := item.Owner.Entry
		return item.Owner.Entry, "owned", &id, &ce
	}
	if item.Owner.Kind == "image" {
		return item.Name, "discovered", nil, nil
	}
	name = item.Name
	if item.SystemdUnit != nil && serviceNameRe.MatchString(*item.SystemdUnit) {
		name = *item.SystemdUnit
	}
	return name, "discovered", nil, nil
}

// PlanJSON is what a confirmed write is compared with: PlanBody when set,
// else ImageRaw.
func (w CatalogWrite) PlanJSON() json.RawMessage {
	if w.PlanBody != nil {
		return w.PlanBody
	}
	return w.ImageRaw
}
