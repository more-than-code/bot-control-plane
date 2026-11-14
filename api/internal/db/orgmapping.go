package db

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

// OrgMapping is the reviewed production org mapping that migration 0015
// reads (spec-organizations §4.1, D-7). It names projects by slug; hosts
// follow their former owning project. It holds no secrets.
type OrgMapping struct {
	// RequireAllProjects makes 0015 refuse to run when a live project is not
	// named, so a forgotten project cannot land in a personal org.
	RequireAllProjects bool            `json:"require_all_projects"`
	Orgs               []OrgMappingOrg `json:"orgs"`
}

// OrgMappingOrg is one org and the projects that go into it.
type OrgMappingOrg struct {
	Slug       string   `json:"slug"`
	Name       string   `json:"name"`
	OwnerEmail string   `json:"owner_email"`
	Projects   []string `json:"projects"`
}

var (
	mappingOrgSlug     = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
	mappingProjectSlug = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,79}$`)
)

// maxMappingBytes caps the mapping file.
const maxMappingBytes = 1 << 20

// LoadOrgMapping reads and checks a mapping file. Errors name slugs, never
// the owner emails.
func LoadOrgMapping(path string) (*OrgMapping, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, errors.New("org mapping: cannot open the file")
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, maxMappingBytes+1))
	if err != nil {
		return nil, errors.New("org mapping: cannot read the file")
	}
	if len(raw) > maxMappingBytes {
		return nil, errors.New("org mapping: file is too large")
	}
	return ParseOrgMapping(raw)
}

// ParseOrgMapping decodes and checks a mapping: known keys only, at least
// one org, a valid unique slug per org (not the u- prefix of personal
// orgs), a name of 1 to 80 characters, an owner email, and every project
// slug valid and named once.
func ParseOrgMapping(raw []byte) (*OrgMapping, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var m OrgMapping
	if err := dec.Decode(&m); err != nil {
		return nil, errors.New("org mapping: invalid JSON or unknown key")
	}
	if dec.More() {
		return nil, errors.New("org mapping: trailing data")
	}
	if len(m.Orgs) == 0 {
		return nil, errors.New("org mapping: no orgs")
	}
	orgs := map[string]bool{}
	projects := map[string]bool{}
	for i, o := range m.Orgs {
		if !mappingOrgSlug.MatchString(o.Slug) || strings.HasPrefix(o.Slug, "u-") {
			return nil, fmt.Errorf("org mapping: org %d has an invalid slug", i+1)
		}
		if orgs[o.Slug] {
			return nil, fmt.Errorf("org mapping: org %s is listed twice", o.Slug)
		}
		orgs[o.Slug] = true
		name := strings.TrimSpace(o.Name)
		if name == "" || utf8.RuneCountInString(name) > 80 || name != o.Name {
			return nil, fmt.Errorf("org mapping: org %s has an invalid name", o.Slug)
		}
		if !strings.Contains(o.OwnerEmail, "@") || strings.ContainsAny(o.OwnerEmail, " \t\r\n") {
			return nil, fmt.Errorf("org mapping: org %s has an invalid owner_email", o.Slug)
		}
		if len(o.Projects) == 0 {
			return nil, fmt.Errorf("org mapping: org %s has no projects", o.Slug)
		}
		for _, p := range o.Projects {
			if !mappingProjectSlug.MatchString(p) {
				return nil, fmt.Errorf("org mapping: org %s has an invalid project slug", o.Slug)
			}
			if projects[p] {
				return nil, fmt.Errorf("org mapping: project %s is in more than one org", p)
			}
			projects[p] = true
		}
	}
	return &m, nil
}

// mappingRow is one project of the mapping as 0015 reads it.
type mappingRow struct {
	ProjectSlug string `json:"project_slug"`
	OrgSlug     string `json:"org_slug"`
	OrgName     string `json:"org_name"`
	OwnerEmail  string `json:"owner_email"`
}

// loadMappingSettings puts the mapping in two session settings that 0015
// reads: deploybot.org_mapping (a JSON array of rows) and
// deploybot.org_mapping_require_all ("true" or "false"). nil sets an empty
// mapping: the default rule only. Settings need no database privilege
// (temp tables would need TEMP), and they live only on conn's session, so
// Migrate must apply every file on that one connection, never through a pool.
func loadMappingSettings(ctx context.Context, conn *pgx.Conn, m *OrgMapping) error {
	rows := []mappingRow{}
	requireAll := "false"
	if m != nil {
		for _, o := range m.Orgs {
			for _, p := range o.Projects {
				rows = append(rows, mappingRow{ProjectSlug: p, OrgSlug: o.Slug, OrgName: o.Name, OwnerEmail: o.OwnerEmail})
			}
		}
		if m.RequireAllProjects {
			requireAll = "true"
		}
	}
	raw, err := json.Marshal(rows)
	if err != nil {
		return errors.New("migrate: cannot encode the org mapping")
	}
	var ignored string
	if err := conn.QueryRow(ctx, `SELECT set_config('deploybot.org_mapping', $1, false)`, string(raw)).Scan(&ignored); err != nil {
		return errors.New("migrate: cannot load the org mapping")
	}
	if err := conn.QueryRow(ctx, `SELECT set_config('deploybot.org_mapping_require_all', $1, false)`, requireAll).Scan(&ignored); err != nil {
		return errors.New("migrate: cannot load the org mapping options")
	}
	return nil
}
