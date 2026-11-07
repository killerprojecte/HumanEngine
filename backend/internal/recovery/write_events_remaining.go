package recovery

// These typed intents follow the source schemas' field order, including nested
// objects. Float64 models JavaScript numbers; the encoder rejects nonfinite
// values and the domain validators enforce integer/safe-range constraints.
// None of these domains is an always-append domain in RecoveryAuthority.append.
type ExperienceTrial struct {
	Operation              string   `json:"operation"`
	PolicyID               string   `json:"policyId"`
	SceneID                string   `json:"sceneId"`
	ExpectedPolicyVersion  float64  `json:"expectedPolicyVersion"`
	NextPolicyVersion      float64  `json:"nextPolicyVersion"`
	ExpectedReceiptVersion *float64 `json:"expectedReceiptVersion"`
	NextReceiptVersion     *float64 `json:"nextReceiptVersion"`
	BeforeHash             *string  `json:"beforeHash"`
	AfterHash              *string  `json:"afterHash"`
	NextPhase              *string  `json:"nextPhase,omitempty"`
	NextReason             *string  `json:"nextReason,omitempty"`
}

func (e ExperienceTrial) recoveryRecord() (string, any) {
	return "experience_trial", struct {
		Kind          string `json:"kind"`
		SchemaVersion int    `json:"schemaVersion"`
		ExperienceTrial
	}{"experience_trial", 1, e}
}

type SemanticNextState struct {
	Status    string `json:"status"`
	Admission string `json:"admission"`
}
type SemanticMutation struct {
	Operation       string             `json:"operation"`
	ID              string             `json:"id"`
	SceneID         string             `json:"sceneId"`
	PreviousVersion *float64           `json:"previousVersion"`
	NextVersion     *float64           `json:"nextVersion"`
	BeforeHash      *string            `json:"beforeHash"`
	AfterHash       *string            `json:"afterHash"`
	NextState       *SemanticNextState `json:"nextState,omitempty"`
}

func (e SemanticMutation) recoveryRecord() (string, any) {
	return "semantic_mutation", struct {
		Kind          string `json:"kind"`
		SchemaVersion int    `json:"schemaVersion"`
		SemanticMutation
	}{"semantic_mutation", 1, e}
}

type IdentityEndpoint struct {
	AdapterID string `json:"adapterId"`
	AccountID string `json:"accountId"`
	PersonID  string `json:"personId"`
}
type IdentityTargets struct {
	Endpoints []IdentityEndpoint `json:"endpoints"`
	AliasIDs  []string           `json:"aliasIds"`
	Source    *Source            `json:"source,omitempty"`
}
type IdentityMutation struct {
	Operation         string          `json:"operation"`
	OperationID       string          `json:"operationId"`
	ExpectedRevision  float64         `json:"expectedRevision"`
	NextRevision      float64         `json:"nextRevision"`
	BeforeHash        string          `json:"beforeHash"`
	QualificationHash string          `json:"qualificationHash"`
	Targets           IdentityTargets `json:"targets"`
}

func (e IdentityMutation) recoveryRecord() (string, any) {
	// Nil slices represent the domain's empty lists, never nullable fields.
	if e.Targets.Endpoints == nil {
		e.Targets.Endpoints = []IdentityEndpoint{}
	}
	if e.Targets.AliasIDs == nil {
		e.Targets.AliasIDs = []string{}
	}
	return "identity_mutation", struct {
		Kind          string `json:"kind"`
		SchemaVersion int    `json:"schemaVersion"`
		IdentityMutation
	}{"identity_mutation", 1, e}
}

type WorkingSetReference struct {
	Kind            string   `json:"kind"`
	ID              string   `json:"id"`
	Version         float64  `json:"version"`
	DeletionEpoch   float64  `json:"deletionEpoch"`
	MutationVersion *float64 `json:"mutationVersion,omitempty"`
}
type WorkingSetSelection struct {
	Ref           WorkingSetReference `json:"ref"`
	DependencyKey string              `json:"dependencyKey"`
}
type WorkingSetPinPolicy struct {
	SchemaVersion  int                 `json:"schemaVersion"`
	SceneID        string              `json:"sceneId"`
	Selection      WorkingSetSelection `json:"selection"`
	PolicyRevision float64             `json:"policyRevision"`
	Pinned         bool                `json:"pinned"`
	Terminal       bool                `json:"terminal"`
}
type WorkingSetPin struct {
	Policy WorkingSetPinPolicy `json:"policy"`
}

func (e WorkingSetPin) recoveryRecord() (string, any) {
	return "working_set_pin", struct {
		Kind          string `json:"kind"`
		SchemaVersion int    `json:"schemaVersion"`
		WorkingSetPin
	}{"working_set_pin", 1, e}
}

type CommitmentTerminal struct {
	Version float64 `json:"version"`
	State   string  `json:"state"`
}
type CommitmentMarkerHead struct {
	ID                   string              `json:"id"`
	SceneID              string              `json:"sceneId"`
	LatestVersion        float64             `json:"latestVersion"`
	ActiveVersion        *float64            `json:"activeVersion"`
	EverActivatedVersion *float64            `json:"everActivatedVersion"`
	EverTerminal         *CommitmentTerminal `json:"everTerminal"`
	DeletionEpoch        float64             `json:"deletionEpoch"`
	MutationVersion      float64             `json:"mutationVersion"`
	Deleted              bool                `json:"deleted"`
}
type CommitmentPrivacyMarker struct {
	RecordedAt string               `json:"recordedAt"`
	Head       CommitmentMarkerHead `json:"head"`
	Reason     string               `json:"reason"`
}
type CommitmentHead struct {
	SchemaVersion     int                      `json:"schemaVersion"`
	SceneID           string                   `json:"sceneId"`
	ID                string                   `json:"id"`
	DeletionEpoch     float64                  `json:"deletionEpoch"`
	TemporalVersion   *float64                 `json:"temporalVersion,omitempty"`
	PrivacyMarker     *CommitmentPrivacyMarker `json:"privacyMarker,omitempty"`
	TermsVersion      float64                  `json:"termsVersion"`
	MutationVersion   float64                  `json:"mutationVersion"`
	QualificationHash string                   `json:"qualificationHash"`
}

func (e CommitmentHead) recoveryRecord() (string, any) {
	// Zod's spread of common fields puts kind between temporal fields and versions.
	return "commitment_head", struct {
		SchemaVersion     int                      `json:"schemaVersion"`
		SceneID           string                   `json:"sceneId"`
		ID                string                   `json:"id"`
		DeletionEpoch     float64                  `json:"deletionEpoch"`
		TemporalVersion   *float64                 `json:"temporalVersion,omitempty"`
		PrivacyMarker     *CommitmentPrivacyMarker `json:"privacyMarker,omitempty"`
		Kind              string                   `json:"kind"`
		TermsVersion      float64                  `json:"termsVersion"`
		MutationVersion   float64                  `json:"mutationVersion"`
		QualificationHash string                   `json:"qualificationHash"`
	}{e.SchemaVersion, e.SceneID, e.ID, e.DeletionEpoch, e.TemporalVersion, e.PrivacyMarker, "commitment_head", e.TermsVersion, e.MutationVersion, e.QualificationHash}
}

type CommitmentDelete struct {
	SceneID       string  `json:"sceneId"`
	ID            string  `json:"id"`
	DeletionEpoch float64 `json:"deletionEpoch"`
}

func (e CommitmentDelete) recoveryRecord() (string, any) {
	return "commitment_delete", struct {
		SchemaVersion int `json:"schemaVersion"`
		CommitmentDelete
		Kind string `json:"kind"`
	}{1, e, "commitment_delete"}
}
