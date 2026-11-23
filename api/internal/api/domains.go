package api

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/more-than-code/bot-control-plane/api/internal/audit"
	"github.com/more-than-code/bot-control-plane/api/internal/catalog"
	"github.com/more-than-code/bot-control-plane/api/internal/secret"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// Org domains (api-v1 v1.35 item 1): console only (the routes are
// delegatedDeny and delegatedDenyRead), org owner or admin.

// TXTResolver looks up DNS TXT records. net.Resolver satisfies it.
type TXTResolver interface {
	LookupTXT(ctx context.Context, name string) ([]string, error)
}

const (
	txtPrefix        = "_deploybot."
	txtValuePrefix   = "deploybot-verify="
	maxAssignments   = 64
	domainCheckEvery = 24 * time.Hour
	dnsTimeout       = 10 * time.Second
	// domainCheckBatch is how many domains one re-check pass looks at.
	domainCheckBatch = 200
)

const (
	msgDomainTaken  = "this domain is verified by another organization"
	msgDomainAdded  = "this domain is already added"
	msgDomainName   = "invalid domain name: a lowercase domain such as example.com (IDNA A-labels), not an IP address"
	msgAssignShape  = "assignments name projects of this organization, each once per pattern; a pattern is @ (the name itself), * (one label below it), a name below it such as app or api.dev, or *. and such a name (one label below that name), at most 253 characters written out"
	msgAssignTooBig = "at most 64 assignments per domain"
)

// dnsLabelRe is one LDH label (IDNA A-labels such as xn--… included).
var dnsLabelRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// validHostname is a lowercase DNS name of at least two labels, at most
// 253 characters, whose last label is not all digits (so not an IPv4).
func validHostname(name string) bool {
	if len(name) == 0 || len(name) > 253 || net.ParseIP(name) != nil {
		return false
	}
	labels := strings.Split(name, ".")
	if len(labels) < 2 {
		return false
	}
	for _, l := range labels {
		if !dnsLabelRe.MatchString(l) {
			return false
		}
	}
	return strings.Trim(labels[len(labels)-1], "0123456789") != ""
}

// domainView is one item of GET /v1/orgs/{id}/domains.
type domainView struct {
	ID          uuid.UUID                `json:"id"`
	Name        string                   `json:"name"`
	Status      string                   `json:"status"`
	TXTName     string                   `json:"txt_name"`
	TXTValue    string                   `json:"txt_value"`
	VerifiedAt  *time.Time               `json:"verified_at"`
	LastCheckAt *time.Time               `json:"last_check_at"`
	Assignments []store.DomainAssignment `json:"assignments"`
}

func viewDomain(d store.OrgDomain) domainView {
	a := d.Assignments
	if a == nil {
		a = []store.DomainAssignment{}
	}
	return domainView{
		ID: d.ID, Name: d.Name, Status: d.Status,
		TXTName: txtPrefix + d.Name, TXTValue: txtValuePrefix + d.Token,
		VerifiedAt: d.VerifiedAt, LastCheckAt: d.LastCheckAt, Assignments: a,
	}
}

func (s *Server) resolver() TXTResolver {
	if s.Resolver != nil {
		return s.Resolver
	}
	return net.DefaultResolver
}

// errDNSLookup is a TXT lookup that failed for a reason other than "no
// such record" (a timeout, a server failure): no answer either way.
var errDNSLookup = errors.New("the DNS lookup failed; try again")

// txtFound reports whether the domain's TXT record holds its token. A name
// with no TXT record (NXDOMAIN or no data) is false. Any other lookup error
// is errDNSLookup, so a resolver outage never counts as a failure (review 7).
func (s *Server) txtFound(ctx context.Context, d store.OrgDomain) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, dnsTimeout)
	defer cancel()
	values, err := s.resolver().LookupTXT(ctx, txtPrefix+d.Name)
	if err != nil {
		var dnsErr *net.DNSError
		if errors.As(err, &dnsErr) && dnsErr.IsNotFound {
			return false, nil
		}
		return false, errDNSLookup
	}
	want := txtValuePrefix + d.Token
	for _, v := range values {
		if strings.TrimSpace(v) == want {
			return true, nil
		}
	}
	return false, nil
}

// orgDomain loads the path's domain of the gated org.
func (s *Server) orgDomain(w http.ResponseWriter, r *http.Request, orgID uuid.UUID) (store.OrgDomain, bool) {
	domainID, ok := s.pathID(w, r, "domain_id")
	if !ok {
		return store.OrgDomain{}, false
	}
	d, err := s.Store.GetOrgDomain(r.Context(), orgID, domainID)
	if err != nil {
		s.fail(w, err)
		return store.OrgDomain{}, false
	}
	return d, true
}

func (s *Server) handleDomainList(w http.ResponseWriter, r *http.Request) {
	p, ok := s.require(w, r)
	if !ok {
		return
	}
	orgID, _, ok := s.orgGate(w, r, p, orgNeedAdmin, "")
	if !ok {
		return
	}
	domains, err := s.Store.ListOrgDomains(r.Context(), orgID)
	if err != nil {
		s.fail(w, err)
		return
	}
	items := make([]domainView, len(domains))
	for i, d := range domains {
		items[i] = viewDomain(d)
	}
	writeOK(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handleDomainCreate(w http.ResponseWriter, r *http.Request) {
	p, ok := s.require(w, r)
	if !ok {
		return
	}
	orgID, _, ok := s.orgGate(w, r, p, orgNeedAdmin, audit.ActionDomainAdd)
	if !ok {
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if !validHostname(body.Name) {
		writeCode(w, "bad_request", msgDomainName)
		return
	}
	token, _, err := secret.NewToken()
	if err != nil {
		s.fail(w, err)
		return
	}
	d, err := s.Store.CreateOrgDomain(r.Context(), orgID, body.Name, token, p.User.ID, s.now())
	switch {
	case errors.Is(err, store.ErrDomainTaken):
		writeCode(w, "conflict", msgDomainTaken)
		return
	case errors.Is(err, store.ErrConflict):
		writeCode(w, "conflict", msgDomainAdded)
		return
	case err != nil:
		s.fail(w, err)
		return
	}
	s.auditOK(r.Context(), p, store.AuditRow{OrgID: idPtr(orgID), Action: audit.ActionDomainAdd, Detail: map[string]any{"domain": d.Name}})
	writeOK(w, http.StatusCreated, viewDomain(d))
}

func (s *Server) handleDomainVerify(w http.ResponseWriter, r *http.Request) {
	p, ok := s.require(w, r)
	if !ok {
		return
	}
	orgID, _, ok := s.orgGate(w, r, p, orgNeedAdmin, audit.ActionDomainVerify)
	if !ok {
		return
	}
	d, ok := s.orgDomain(w, r, orgID)
	if !ok {
		return
	}
	found, err := s.txtFound(r.Context(), d)
	if err != nil {
		writeCode(w, "unavailable", err.Error())
		return
	}
	if !found {
		// Last check shows this attempt (pending or lapsed only; a verified
		// domain's daily re-check keeps its own clock).
		if err := s.Store.TouchDomainCheck(r.Context(), orgID, d.ID, s.now()); err != nil {
			s.logger().Error("domain check time not stored")
		}
		writeCode(w, "conflict", fmt.Sprintf("TXT record not found yet: add %s = %s", txtPrefix+d.Name, txtValuePrefix+d.Token))
		return
	}
	verified, err := s.Store.MarkDomainVerified(r.Context(), orgID, d.ID, s.now())
	if errors.Is(err, store.ErrDomainTaken) {
		writeCode(w, "conflict", msgDomainTaken)
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	s.auditOK(r.Context(), p, store.AuditRow{OrgID: idPtr(orgID), Action: audit.ActionDomainVerify, Detail: map[string]any{"domain": d.Name}})
	if d.Status == store.DomainLapsed {
		// Lapsed routes kept serving; nothing new goes live, but re-check.
		s.kickOrgEdges(r.Context(), orgID)
	}
	writeOK(w, http.StatusOK, viewDomain(verified))
}

func (s *Server) handleDomainAssign(w http.ResponseWriter, r *http.Request) {
	p, ok := s.require(w, r)
	if !ok {
		return
	}
	orgID, _, ok := s.orgGate(w, r, p, orgNeedAdmin, audit.ActionDomainAssign)
	if !ok {
		return
	}
	d, ok := s.orgDomain(w, r, orgID)
	if !ok {
		return
	}
	var body struct {
		Assignments []struct {
			ProjectID string `json:"project_id"`
			Pattern   string `json:"pattern"`
		} `json:"assignments"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if len(body.Assignments) > maxAssignments {
		writeCode(w, "bad_request", msgAssignTooBig)
		return
	}
	in := make([]store.DomainAssignment, 0, len(body.Assignments))
	for _, a := range body.Assignments {
		id, err := uuid.Parse(a.ProjectID)
		if err != nil || !store.ValidPattern(d.Name, a.Pattern) {
			writeCode(w, "bad_request", msgAssignShape)
			return
		}
		in = append(in, store.DomainAssignment{ProjectID: id, Pattern: a.Pattern})
	}
	out, err := s.Store.PutDomainAssignments(r.Context(), orgID, d.ID, in)
	if errors.Is(err, store.ErrInvalid) {
		writeCode(w, "bad_request", msgAssignShape)
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	detail := []map[string]any{}
	for _, a := range out.Assignments {
		detail = append(detail, map[string]any{"project_id": a.ProjectID.String(), "pattern": a.Pattern})
	}
	s.auditOK(r.Context(), p, store.AuditRow{OrgID: idPtr(orgID), Action: audit.ActionDomainAssign, Detail: map[string]any{"domain": d.Name, "assignments": detail}})
	s.kickOrgEdges(r.Context(), orgID)
	writeOK(w, http.StatusOK, viewDomain(out))
}

// domainDeletePlan names the domain and every route that would stop.
func domainDeletePlan(name string, routes []store.Route, projectNames map[uuid.UUID]string) string {
	if len(routes) == 0 {
		return "remove domain " + name + "; no routes stop"
	}
	parts := make([]string, len(routes))
	for i, rt := range routes {
		parts[i] = fmt.Sprintf("%s (project %s, entry %s)", routeURL(rt.Hostname, rt.PathPrefix), projectNames[rt.ProjectID], rt.Entry)
	}
	return "remove domain " + name + "; routes that stop:\n" + strings.Join(parts, "\n")
}

func domainDeleteRequestHash(orgID, domainID uuid.UUID) []byte {
	sum := sha256.Sum256([]byte(`{"action":"domain.delete","domain_id":` + jsonQuote(domainID.String()) + `,"org_id":` + jsonQuote(orgID.String()) + `}`))
	return sum[:]
}

// handleDomainDelete answers 202 with a plan naming the routes that stop;
// POST /v1/confirmations/{id} applies it.
func (s *Server) handleDomainDelete(w http.ResponseWriter, r *http.Request) {
	p, ok := s.require(w, r)
	if !ok {
		return
	}
	orgID, _, ok := s.orgGate(w, r, p, orgNeedAdmin, audit.ActionDomainDelete)
	if !ok {
		return
	}
	domainID, ok := s.pathID(w, r, "domain_id")
	if !ok {
		return
	}
	var body struct {
		Key string `json:"idempotency_key"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if !catalog.ValidIdempotencyKey(body.Key) {
		writeCode(w, "bad_request", "invalid idempotency_key")
		return
	}
	reqHash := domainDeleteRequestHash(orgID, domainID)
	d, err := s.Store.GetOrgDomain(r.Context(), orgID, domainID)
	if errors.Is(err, store.ErrNotFound) {
		// A domain the confirm already removed replays this key's answer.
		if s.replayStoredIdempotency(w, r, p.User.ID, body.Key, reqHash) {
			return
		}
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	routes, err := s.Store.RoutesUnderDomain(r.Context(), orgID, d.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	names := map[uuid.UUID]string{}
	for _, rt := range routes {
		if _, ok := names[rt.ProjectID]; ok {
			continue
		}
		if project, err := s.Store.GetProject(r.Context(), rt.ProjectID); err == nil {
			names[rt.ProjectID] = project.Name
		}
	}
	plan := domainDeletePlan(d.Name, routes, names)
	// The plan is bound to the routes it names (review 4).
	planBody, _ := json.Marshal(map[string]any{"domain": d.Name, "routes": store.RouteKeys(routes)})
	s.issueEdgePlan(w, r, p, store.EdgeConfirmation{
		Kind: store.EdgePlanDomainDelete, OrgID: idPtr(orgID), DomainID: idPtr(d.ID), Body: planBody,
	}, body.Key, reqHash, plan, store.AuditRow{
		OrgID: idPtr(orgID), Action: audit.ActionDomainDeleteConfirmation, Detail: map[string]any{"domain": d.Name, "routes": len(routes)},
	})
}

// RecheckDomains runs one pass of the daily re-check (spec D4): every
// verified or lapsed domain not checked in the last 24 hours is looked up
// once. Seven failures in a row make a verified domain lapsed; a success
// makes it verified again. A lookup that fails for another reason than a
// missing record is skipped and retried on the next pass. It returns how
// many domains it checked. It is idempotent within a day.
func (s *Server) RecheckDomains(ctx context.Context) int {
	now := s.now()
	due, err := s.Store.ListDomainsDue(ctx, now.Add(-domainCheckEvery), domainCheckBatch)
	if err != nil {
		s.logger().Error("domain re-check list failed")
		return 0
	}
	checked := 0
	for _, d := range due {
		ok, err := s.txtFound(ctx, d)
		if err != nil {
			// Not an answer: try again next pass, failures unchanged.
			continue
		}
		checked++
		after, lapsed, err := s.Store.RecordDomainCheck(ctx, d.ID, ok, now)
		if err != nil {
			continue
		}
		if lapsed {
			s.Audit.Record(ctx, store.AuditRow{
				Via: audit.ViaSystem, OrgID: idPtr(after.OrgID), Action: audit.ActionDomainLapsed,
				Decision: audit.DecisionAllowed, Detail: map[string]any{"domain": after.Name, "failures": after.Failures},
			})
		}
	}
	return checked
}

// RunDomainChecks calls RecheckDomains every hour until ctx ends. Each
// domain is looked up at most once a day.
func (s *Server) RunDomainChecks(ctx context.Context) {
	tick := time.NewTicker(time.Hour)
	defer tick.Stop()
	for {
		s.RecheckDomains(ctx)
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}
