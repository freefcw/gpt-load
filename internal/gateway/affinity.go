package gateway

import (
	"gpt-load/internal/affinity"
	"gpt-load/internal/channel"
	"gpt-load/internal/protocol"
	"gpt-load/internal/scheduler"
	"gpt-load/internal/state"
	"gpt-load/internal/telemetry"
)

type requestAffinity struct {
	key                   affinity.Key
	observation           affinity.Observation
	preferredCredentialID uint
	continuityKey         string
	kind                  string
}

func (handler *Handler) resolveRequestAffinity(
	snapshot *state.ConfigSnapshot,
	accessKeyID uint,
	clientProtocol protocol.Protocol,
	prefix []byte,
	allowedCredentialRefs map[uint]state.CredentialRef,
	promptCacheKey string,
	sessionID string,
) requestAffinity {
	if handler == nil || snapshot == nil {
		return requestAffinity{}
	}
	continuityKey := affinity.DeriveKey(
		handler.encryption,
		accessKeyID,
		clientProtocol,
		prefix,
	)
	// 执行层私有 replay scope 仍由提示词派生；它与账号路由 owner 是两个不同概念。
	result := requestAffinity{continuityKey: string(continuityKey), kind: telemetry.AffinityPromptPrefix}
	var key affinity.Key
	if sessionID != "" {
		key = affinity.DeriveSessionID(handler.encryption, accessKeyID, clientProtocol, sessionID)
		result.kind = telemetry.AffinitySessionID
	} else if hasNonCodexCandidate(snapshot, allowedCredentialRefs) {
		if promptCacheKey != "" {
			// prompt_cache_key is a cache hint. Codex-only candidate pools must not
			// turn one shared cache key into a permanent account owner.
			key = affinity.DerivePromptCacheKey(handler.encryption, accessKeyID, clientProtocol, promptCacheKey)
			result.kind = telemetry.AffinityPromptCacheKey
		} else {
			key = continuityKey
		}
	}
	if handler.affinityCache == nil ||
		!handler.affinityCache.Configure(
			snapshot.Revision,
			snapshot.Settings.AffinityCapacity,
			snapshot.Settings.AffinityTTL,
		) {
		return result
	}
	if !key.Valid() {
		return result
	}
	result.key = key
	observation := handler.affinityCache.Lookup(key)
	resolved := result
	resolved.observation = observation
	if !observation.Found() {
		return resolved
	}
	target := observation.Target
	group, exists := snapshot.Groups[target.GroupID]
	if !exists || !group.AffinityEnabled {
		return resolved
	}
	if result.kind != telemetry.AffinitySessionID && group.ChannelID == channel.Codex {
		// 提示词派生的 key 不绑定到 Codex 账号；显式 Session-Id 仍按账号归属。
		return resolved
	}
	ref, allowed := allowedCredentialRefs[target.CredentialID]
	if !allowed || ref.GroupID != target.GroupID ||
		ref.IdentityGeneration != target.IdentityGeneration ||
		ref.ConfigurationRevision != target.ConfigurationRevision {
		return resolved
	}
	resolved.preferredCredentialID = target.CredentialID
	return resolved
}

func hasNonCodexCandidate(
	snapshot *state.ConfigSnapshot,
	allowedCredentialRefs map[uint]state.CredentialRef,
) bool {
	if snapshot == nil {
		return false
	}
	for _, ref := range allowedCredentialRefs {
		group, exists := snapshot.Groups[ref.GroupID]
		if exists && group.ChannelID != channel.Codex {
			return true
		}
	}
	return false
}

func (handler *Handler) recordAffinitySuccess(
	request requestAffinity,
	selection scheduler.Selection,
	ref state.CredentialRef,
) {
	if handler == nil || handler.affinityCache == nil || !request.key.Valid() ||
		!selection.Group.AffinityEnabled {
		return
	}
	if request.kind != telemetry.AffinitySessionID && selection.Group.ChannelID == channel.Codex {
		return
	}
	handler.affinityCache.RecordSuccess(
		request.key,
		request.observation,
		affinity.Target{
			GroupID: selection.GroupID, CredentialID: selection.CredentialID,
			IdentityGeneration:    ref.IdentityGeneration,
			ConfigurationRevision: ref.ConfigurationRevision,
		},
	)
}
