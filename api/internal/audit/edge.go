package audit

// Domains, the per-host edge and routes (api-v1 v1.35).
const (
	ActionDomainAdd                = "domain.add"
	ActionDomainVerify             = "domain.verify"
	ActionDomainAssign             = "domain.assign"
	ActionDomainDelete             = "domain.delete"
	ActionDomainLapsed             = "domain.lapsed"
	ActionDomainDeleteConfirmation = "domain.delete_confirmation_issued"
	ActionEdgeEnable               = "edge.enable"
	ActionEdgeDisable              = "edge.disable"
	ActionRoutesUpdate             = "routes.update"
	ActionRoutesConfirmation       = "routes.write_confirmation_issued"
	ActionOrgACMEEmail             = "org.acme_email"
)
