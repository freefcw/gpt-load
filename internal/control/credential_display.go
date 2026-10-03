package control

// credentialDisplayResponse carries operator-facing metadata separately from
// credential_name, which remains the masked/email identity used by existing
// APIs and audit consumers.
type credentialDisplayResponse struct {
	CredentialAlias          string `json:"credential_alias,omitempty"`
	CredentialConnectionType string `json:"credential_connection_type,omitempty"`
}

func (s *Service) credentialDisplay(credentialID *uint) credentialDisplayResponse {
	if credentialID == nil || s == nil || s.registry == nil || s.manager == nil {
		return credentialDisplayResponse{}
	}
	ref, exists := s.registry.CredentialRef(*credentialID)
	if !exists {
		return credentialDisplayResponse{}
	}
	result := credentialDisplayResponse{CredentialAlias: ref.Name}
	if snapshot := s.manager.Current(); snapshot != nil {
		if group, exists := snapshot.GroupCatalog[ref.GroupID]; exists {
			result.CredentialConnectionType = group.ConnectionType
		}
	}
	return result
}

func (s *Service) decorateRequestLogCredential(item *requestLogItemResponse, autoCredentialID uint) {
	if item == nil {
		return
	}
	item.credentialDisplayResponse = s.credentialDisplay(item.CredentialID)
	if item.AutoDecision != nil {
		item.AutoDecision.credentialDisplayResponse = s.credentialDisplay(&autoCredentialID)
	}
}
