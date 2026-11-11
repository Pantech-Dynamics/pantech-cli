package api

import "encoding/json"

// Teams and access policies. Only what the CLI shows is here; --json prints
// the API's own body.

// IAMAction is one entry of the action catalogue.
type IAMAction struct {
	Action        string   `json:"action"`
	Service       string   `json:"service"`
	Description   string   `json:"description"`
	ResourceTypes []string `json:"resource_types"`
	Read          bool     `json:"read"`
}

// IAMSettings is the organization's access setting.
type IAMSettings struct {
	MemberBaseline string  `json:"member_baseline"`
	Version        int     `json:"version"`
	UpdatedAt      *string `json:"updated_at"`
}

// IAMTeam is a named group of members.
type IAMTeam struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	MemberCount int      `json:"member_count"`
	PolicyIDs   []string `json:"policy_ids"`
	CreatedAt   *string  `json:"created_at"`
}

// IAMTeamMember is one member of a team.
type IAMTeamMember struct {
	UserID  string  `json:"user_id"`
	Email   string  `json:"email"`
	Name    string  `json:"name"`
	Role    string  `json:"role"`
	AddedAt *string `json:"added_at"`
}

// PolicyConditions must all hold for a statement to apply.
type PolicyConditions struct {
	SourceIP []string `json:"source_ip,omitempty"`
	MFA      *bool    `json:"mfa,omitempty"`
}

// PolicyStatement is one statement of a policy.
type PolicyStatement struct {
	Sid        string            `json:"sid,omitempty"`
	Effect     string            `json:"effect"`
	Actions    []string          `json:"actions"`
	Resources  []string          `json:"resources"`
	Conditions *PolicyConditions `json:"conditions,omitempty"`
}

// IAMPolicy is a policy; Managed ones are Pantech's and cannot change.
type IAMPolicy struct {
	ID              string            `json:"id"`
	Name            string            `json:"name"`
	Description     string            `json:"description"`
	Managed         bool              `json:"managed"`
	Statements      []PolicyStatement `json:"statements"`
	AttachmentCount int               `json:"attachment_count"`
	UpdatedAt       *string           `json:"updated_at"`
	Version         int               `json:"version"`
}

// IAMAttachment is a policy attached to a member or a team.
type IAMAttachment struct {
	PolicyID      string  `json:"policy_id"`
	PrincipalType string  `json:"principal_type"`
	PrincipalID   string  `json:"principal_id"`
	AttachedAt    *string `json:"attached_at"`
}

// IAMEffectivePolicy is a policy that applies to a member, and the team that
// brought it (nil when attached directly).
type IAMEffectivePolicy struct {
	PolicyID string  `json:"policy_id"`
	TeamID   *string `json:"team_id"`
}

// IAMEffectiveAccess is what applies to a member.
type IAMEffectiveAccess struct {
	UserID         string               `json:"user_id"`
	Role           string               `json:"role"`
	Teams          []string             `json:"teams"`
	Policies       []IAMEffectivePolicy `json:"policies"`
	MemberBaseline string               `json:"member_baseline"`
}

// IAMSimulation is the decision the evaluator would make.
type IAMSimulation struct {
	Allowed  bool    `json:"allowed"`
	Decision string  `json:"decision"`
	PolicyID *string `json:"policy_id"`
	Sid      *string `json:"sid"`
}

// PolicyDocument is what a policy file holds: the body of a create or an
// update. Statements stay raw so the API, which owns the rules, judges them.
type PolicyDocument struct {
	Name        string            `json:"name,omitempty"`
	Description *string           `json:"description,omitempty"`
	Statements  []json.RawMessage `json:"statements"`
}

// IAMMember is one member of the organization, with their teams.
type IAMMember struct {
	UserID      string   `json:"user_id"`
	Email       string   `json:"email"`
	Name        string   `json:"name"`
	Role        string   `json:"role"`
	TeamIDs     []string `json:"team_ids"`
	PolicyCount int      `json:"policy_count"`
}
