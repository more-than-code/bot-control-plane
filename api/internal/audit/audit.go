// Package audit writes append-only audit rows. It never records tokens.
package audit

import (
	"context"
	"log/slog"
	"time"

	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

const (
	ActionSignIn          = "sign_in"
	ActionProjectCreate   = "project.create"
	ActionProjectRename   = "project.rename"
	ActionProjectDelete   = "project.delete"
	ActionProjectTransfer = "project.transfer"
	ActionMemberAdd       = "member.add"
	ActionMemberUpdate    = "member.update"
	ActionMemberRemove    = "member.remove"
	ActionJoinCreate      = "join_token.create"
	ActionJoinRevoke      = "join_token.revoke"
	ActionInviteCreate    = "invite.create"
	ActionInviteRevoke    = "invite.revoke"
	ActionInviteAccept    = "invite.accept"
	ActionHostEnroll      = "host.enroll"
	ActionHostRevoke      = "host.revoke"
	ActionHostRename      = "host.rename"
	ActionHostArchive     = "host.archive"
	// ActionHostAlertSettings is a change of a host's alert settings
	// (api-v1 v1.49 item 3). Detail: host and the changed fields.
	ActionHostAlertSettings = "host.alert_settings"
	// api-v1 v1.24 item 8. The old host.share* and catalog approval
	// actions stay readable in history; nothing writes them now.
	ActionHostAccessGrant          = "host.access_grant"
	ActionHostAccessUpdate         = "host.access_update"
	ActionHostAccessRevoke         = "host.access_revoke"
	ActionOrgCreate                = "org.create"
	ActionOrgRename                = "org.rename"
	ActionOrgDelete                = "org.delete"
	ActionOrgTransfer              = "org.transfer"
	ActionOrgMemberAdd             = "org_member.add"
	ActionOrgMemberUpdate          = "org_member.update"
	ActionOrgMemberRemove          = "org_member.remove"
	ActionOrgJoinCreate            = "org_join_token.create"
	ActionOrgJoinRevoke            = "org_join_token.revoke"
	ActionHealthChecks             = "health_checks.replace"
	ActionHealthConfirmation       = "health_checks.confirmation_issued"
	ActionServiceRename            = "service.rename"
	ActionSnapshot                 = "host.snapshot"
	ActionProbe                    = "service.probe"
	ActionDeviceApprove            = "device.approve"
	ActionDeviceDeny               = "device.deny"
	ActionOAuthRegister            = "oauth.client_register"
	ActionOAuthAuthorize           = "oauth.authorize"
	ActionOAuthTokenRevoke         = "oauth.token_revoke"
	ActionOAuthRefreshReuse        = "oauth.refresh_reuse"
	ActionCatalogCreate            = "catalog.create"
	ActionCatalogUpdate            = "catalog.update"
	ActionCatalogDelete            = "catalog.delete"
	ActionCatalogWriteConfirmation = "catalog.write_confirmation_issued"
	ActionCatalogPermissions       = "catalog.permissions"
	ActionProgramRun               = "program.run"
	ActionProgramStop              = "program.stop"
	ActionProgramRestart           = "program.restart"
	ActionProgramConfirmation      = "program.confirmation_issued"
	ActionCatalogDeploy            = "catalog.deploy"
	ActionCatalogRollback          = "catalog.rollback"
	ActionCatalogRemove            = "catalog.remove"
	ActionCatalogAutoRestart       = "catalog.auto_restart"
	ActionCatalogAutoRollback      = "catalog.auto_rollback"
	ActionCatalogAutoGaveUp        = "catalog.auto_gave_up"
	ActionEnvSetReplace            = "env_set.replace"
	ActionEnvSetMerge              = "env_set.merge"
	ActionEnvSetKeyPut             = "env_set.key_put"
	ActionEnvSetKeyDelete          = "env_set.key_delete"
	ActionEnvSetKeySecret          = "env_set.key_secret"
	ActionEnvSetKeyReveal          = "env_set.key_reveal"
	// ActionEnvSetKeyRegenerate replaces a secret value with a generated
	// one (api-v1 v1.52); detail set, key, kind and used_by, never a value.
	ActionEnvSetKeyRegenerate     = "env_set.key_regenerate"
	ActionEnvSetCreate            = "env_set.create"
	ActionEnvSetWriteConfirmation = "env_set.write_confirmation_issued"
	ActionEnvSetDelete            = "env_set.delete"
	// ActionEnvSetUpdate is a confirmed update_env_set plan (api-v1 v1.33
	// item 1). Detail names keys per action, never a value.
	ActionEnvSetUpdate = "env_set.update"
	ActionEnvSetList   = "env_set.list"
	ActionEnvSetGet    = "env_set.get"
	// Value requests (api-v1 v1.28). The fill itself is env_set.key_put
	// with detail via "value_request".
	ActionEnvSetValueRequested       = "env_set.value_requested"
	ActionEnvSetValueRequestDeclined = "env_set.value_request_declined"
	ActionRegistryPut                = "registry.put"
	ActionRegistryDelete             = "registry.delete"
	// Connect GitHub (api-v1 v1.29). Detail never holds the token.
	ActionRegistryGitHubConnected    = "registry.github_connected"
	ActionRegistryGitHubDisconnected = "registry.github_disconnected"
	// ActionRegistryGitHubRefreshRejected is GitHub rejecting a stored
	// refresh token (api-v1 v1.43 item 2). Detail is {"host"}.
	ActionRegistryGitHubRefreshRejected = "registry.github_refresh_rejected"
	// ActionRegistryImageVersionDeleted is one ghcr.io package version
	// deleted (api-v1 v1.45 §8). Detail: host, package, version_id, digest,
	// tags, via.
	ActionRegistryImageVersionDeleted = "registry.image_version_deleted"
	ActionFilePut                     = "file.put"
	// Host config files (api-v1 v1.44 item 7). Detail never holds content,
	// a value or a secret file's hash.
	ActionHostFileSave  = "host_file.save"
	ActionHostFileApply = "host_file.apply"
	// Agent upgrades (api-v1 v1.48 item 5). Detail: host, from and to on a
	// start (allowed, or denied with the refusal as error_code); host, to,
	// state and reason when the row settles.
	ActionHostAgentUpgrade       = "host.agent_upgrade"
	ActionHostAgentUpgradeResult = "host.agent_upgrade_result"
	// Console host policy (api-v1 v1.51). Detail: host, the change
	// (edge_from, edge_to, images_added, images_removed, projects_added,
	// projects_removed), the refusal reason on a denied start; host and
	// outcome (applied, rolled_back, unknown) when it settles.
	ActionHostPolicyApply  = "host.policy_apply"
	ActionHostPolicyResult = "host.policy_result"
	ActionFileDelete       = "file.delete"
	// File requests (api-v1 v1.30). The fill itself is file.put with
	// detail via "file_request".
	ActionFileRequested       = "file.requested"
	ActionFileRequestDeclined = "file.request_declined"
	ActionDelegatedDenied     = "delegated.denied"
	// ActionServiceOwnerConflict is an owned services report that would have
	// moved a row of another project, or an org-level row (review A2).
	ActionServiceOwnerConflict = "service.owner_conflict"

	ViaConsole = "console"
	ViaMCP     = "mcp"
	ViaAgent   = "agent"
	ViaSystem  = "system"

	DecisionAllowed = "allowed"
	DecisionDenied  = "denied"
)

// OwnerConflictWindow is how long an identical service.owner_conflict (same
// service, reported project and entry) is not audited again. Agents resend
// their report every 300 s, so a stuck service would otherwise write about
// 288 rows a day (review batch 2, item 30).
const OwnerConflictWindow = 24 * time.Hour

// Writer appends audit rows. A write failure is logged and not returned:
// the caller's HTTP result must not depend on the audit insert succeeding
// after the action has already committed. Callers that need the insert to
// be part of the action use the store directly inside the same transaction.
// Phase 1 records the audit after the action.
type Writer struct {
	Store store.Store
	Log   *slog.Logger
	Now   func() time.Time
}

// Record inserts one audit row. Nil detail becomes an empty object.
func (w Writer) Record(ctx context.Context, row store.AuditRow) {
	if w.Store == nil {
		return
	}
	if row.At.IsZero() && w.Now != nil {
		row.At = w.Now()
	}
	if row.At.IsZero() {
		row.At = time.Now().UTC()
	}
	if row.Detail == nil {
		row.Detail = map[string]any{}
	}
	if err := w.Store.InsertAudit(ctx, row); err != nil {
		log := w.Log
		if log == nil {
			log = slog.Default()
		}
		log.Error("audit write failed", "action", row.Action)
	}
}
