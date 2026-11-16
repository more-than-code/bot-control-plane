package api

import (
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"regexp"

	"github.com/more-than-code/bot-control-plane/api/internal/authz"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// Shared-network snippet (api-v1 v1.33 item 7): the runner.toml lines that
// would open a shared network to this project, as the v1.29 host-policy
// snippet. Text only; nothing changes on any host, and creating a network
// stays with the host's root (v1.25).

// sharedNetworkName is the agent-protocol v1.13 network name rule.
var sharedNetworkName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

const (
	msgNetworkName   = "network must be 1 to 32 lowercase letters, digits or dashes, starting with a letter or digit"
	msgNetworkSubnet = "subnet must be an IPv4 CIDR in canonical form inside 10.0.0.0/8, 172.16.0.0/12 or 192.168.0.0/16, with a prefix of 16 to 28"
)

var privateRanges = []netip.Prefix{
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.168.0.0/16"),
}

// subnetOK is the agent's shared_networks subnet rule: canonical IPv4 CIDR,
// RFC 1918, prefix 16 to 28.
func subnetOK(raw string) bool {
	prefix, err := netip.ParsePrefix(raw)
	if err != nil || !prefix.Addr().Is4() || prefix.Masked() != prefix || prefix.String() != raw {
		return false
	}
	if prefix.Bits() < 16 || prefix.Bits() > 28 {
		return false
	}
	for _, r := range privateRanges {
		if r.Bits() <= prefix.Bits() && r.Contains(prefix.Addr()) {
			return true
		}
	}
	return false
}

func (s *Server) handleNetworkSnippet(w http.ResponseWriter, r *http.Request) {
	p, projectID, ok := s.catalogProject(w, r)
	if !ok {
		return
	}
	if err := authz.Authorize(r.Context(), p.User.ID, projectID, authz.CapMembers); err != nil {
		s.writeAuth(w, err)
		return
	}
	hostID, ok := parseHostID(w, r.PathValue("host_id"))
	if !ok {
		return
	}
	rel, err := authz.HostRelation(r.Context(), p.User.ID, hostID)
	if err != nil {
		s.fail(w, err)
		return
	}
	// The project's deploy access, or an org owner or admin of the host's
	// org: a project with metrics access only learns nothing about the
	// host's networks (review D, 2026-11-16).
	if !rel.OrgAdmin() {
		level, err := s.Store.HostProjectAccess(r.Context(), projectID, hostID)
		if err != nil {
			s.fail(w, err)
			return
		}
		if level != authz.LevelDeploy {
			writeCode(w, "not_found", "not found")
			return
		}
	}
	query := r.URL.Query()
	network, subnet := query.Get("network"), query.Get("subnet")
	if !sharedNetworkName.MatchString(network) {
		writeCode(w, "bad_request", msgNetworkName)
		return
	}
	host, err := s.Store.GetHost(r.Context(), hostID)
	if errors.Is(err, store.ErrNotFound) {
		writeCode(w, "not_found", "not found")
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	// A network the host already defines keeps its subnet; subnet may then
	// be left out, and a different one is refused.
	if host.Programs != nil {
		for _, n := range host.Programs.SharedNetworks {
			if n.Name != network {
				continue
			}
			if subnet == "" {
				subnet = n.Subnet
			} else if subnet != n.Subnet {
				writeCode(w, "bad_request", fmt.Sprintf("network %s already has subnet %s on this host", network, n.Subnet))
				return
			}
		}
	}
	if !subnetOK(subnet) {
		writeCode(w, "bad_request", msgNetworkSubnet)
		return
	}
	project, err := s.Store.GetProject(r.Context(), projectID)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeOK(w, http.StatusOK, map[string]any{"snippet": networkSnippet(network, subnet, project.Slug)})
}

// networkSnippet is the two runner.toml lines, after a comment saying how
// the host's root applies them.
func networkSnippet(network, subnet, slug string) string {
	return "# In /etc/bot-agent/runner.toml, add these items to the lists already there\n" +
		"# (the project slug must also be in projects), then restart bot-agent.\n" +
		fmt.Sprintf("shared_networks = [%q]\n", network+"="+subnet) +
		fmt.Sprintf("shared_network_projects = [%q]\n", network+":"+slug)
}
