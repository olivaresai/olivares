// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// iamAPIVersion is the IAM Query API version.
const iamAPIVersion = "2010-05-08"

// iamItem is one discovered IAM identity or policy. Only metadata is captured:
// the natural name (for attachment correlation) and the ARN (the emitted ref).
// No policy document, key, or other sensitive material is ever read.
type iamItem struct {
	name string
	arn  string
}

// The IAM Query responses share an envelope: <XxxResponse><XxxResult>...
// <IsTruncated/><Marker/></XxxResult></XxxResponse>. We decode each into a
// dedicated struct rather than a generic one to keep the XML mapping explicit.
// Only metadata fields (name, ARN) are mapped; policy documents are never read.

type rolesEnvelope struct {
	XMLName xml.Name `xml:"ListRolesResponse"`
	Result  struct {
		Roles       []iamMember `xml:"Roles>member"`
		IsTruncated bool        `xml:"IsTruncated"`
		Marker      string      `xml:"Marker"`
	} `xml:"ListRolesResult"`
}

type usersEnvelope struct {
	XMLName xml.Name `xml:"ListUsersResponse"`
	Result  struct {
		Users       []iamMember `xml:"Users>member"`
		IsTruncated bool        `xml:"IsTruncated"`
		Marker      string      `xml:"Marker"`
	} `xml:"ListUsersResult"`
}

type policiesEnvelope struct {
	XMLName xml.Name `xml:"ListPoliciesResponse"`
	Result  struct {
		Policies    []iamMember `xml:"Policies>member"`
		IsTruncated bool        `xml:"IsTruncated"`
		Marker      string      `xml:"Marker"`
	} `xml:"ListPoliciesResult"`
}

type attachedPoliciesEnvelope struct {
	XMLName xml.Name `xml:"ListAttachedRolePoliciesResponse"`
	Result  struct {
		AttachedPolicies []attachedPolicyMember `xml:"AttachedPolicies>member"`
		IsTruncated      bool                   `xml:"IsTruncated"`
		Marker           string                 `xml:"Marker"`
	} `xml:"ListAttachedRolePoliciesResult"`
}

// iamMember covers the common name/arn pair across Roles, Users and Policies.
// RoleName/UserName/PolicyName all decode into Name via the union of element
// names; Arn is the resource ref.
type iamMember struct {
	RoleName   string `xml:"RoleName"`
	UserName   string `xml:"UserName"`
	PolicyName string `xml:"PolicyName"`
	Arn        string `xml:"Arn"`
}

// name returns whichever name element was populated for this member.
func (m iamMember) name() string {
	switch {
	case m.RoleName != "":
		return m.RoleName
	case m.UserName != "":
		return m.UserName
	default:
		return m.PolicyName
	}
}

// attachedPolicyMember is one row of ListAttachedRolePolicies: the attached
// managed policy's name and ARN.
type attachedPolicyMember struct {
	PolicyName string `xml:"PolicyName"`
	PolicyArn  string `xml:"PolicyArn"`
}

// gatherIAM runs the IAM inventory pass: list roles, users and policies, then per
// role list its attached managed policies. It emits topology edges in a
// deterministic (sorted) order and returns the first error encountered so the
// caller can record a single health finding for the service. ctx is honored
// between operations and pages.
func (s *Source) gatherIAM(ctx context.Context, sink sdk.Sink, at time.Time) error {
	roles, err := s.listRoles(ctx)
	if err != nil {
		return err
	}
	users, err := s.listUsers(ctx)
	if err != nil {
		return err
	}
	policies, err := s.listPolicies(ctx)
	if err != nil {
		return err
	}

	origin := s.cfg.originAccountRef()

	// account ⊳ role
	sortItems(roles)
	for _, r := range roles {
		if err := emit(ctx, sink, inventoryEdge(originAccount, origin, resIAMRole, r.arn, at)); err != nil {
			return err
		}
	}
	// account ⊳ user
	sortItems(users)
	for _, u := range users {
		if err := emit(ctx, sink, inventoryEdge(originAccount, origin, resIAMUser, u.arn, at)); err != nil {
			return err
		}
	}
	// account ⊳ policy
	sortItems(policies)
	for _, p := range policies {
		if err := emit(ctx, sink, inventoryEdge(originAccount, origin, resIAMPolicy, p.arn, at)); err != nil {
			return err
		}
	}

	// role ⊳ attached-policy (origin ref is the role NAME, per the contract)
	for _, r := range roles {
		if err := ctx.Err(); err != nil {
			return err
		}
		attached, err := s.listAttachedRolePolicies(ctx, r.name)
		if err != nil {
			return err
		}
		sortItems(attached)
		for _, ap := range attached {
			if err := emit(ctx, sink, inventoryEdge(originIAMRole, r.name, resIAMPolicy, ap.arn, at)); err != nil {
				return err
			}
		}
	}
	return nil
}

// listRoles lists all IAM roles, following IsTruncated/Marker pagination.
func (s *Source) listRoles(ctx context.Context) ([]iamItem, error) {
	var out []iamItem
	marker := ""
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		q := url.Values{"Action": {"ListRoles"}, "Version": {iamAPIVersion}}
		if marker != "" {
			q.Set("Marker", marker)
		}
		var env rolesEnvelope
		if err := s.iamCall(ctx, q, &env); err != nil {
			return nil, err
		}
		for _, m := range env.Result.Roles {
			out = append(out, iamItem{name: m.name(), arn: m.Arn})
		}
		if !env.Result.IsTruncated || env.Result.Marker == "" {
			return out, nil
		}
		marker = env.Result.Marker
	}
}

// listUsers lists all IAM users, following pagination.
func (s *Source) listUsers(ctx context.Context) ([]iamItem, error) {
	var out []iamItem
	marker := ""
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		q := url.Values{"Action": {"ListUsers"}, "Version": {iamAPIVersion}}
		if marker != "" {
			q.Set("Marker", marker)
		}
		var env usersEnvelope
		if err := s.iamCall(ctx, q, &env); err != nil {
			return nil, err
		}
		for _, m := range env.Result.Users {
			out = append(out, iamItem{name: m.name(), arn: m.Arn})
		}
		if !env.Result.IsTruncated || env.Result.Marker == "" {
			return out, nil
		}
		marker = env.Result.Marker
	}
}

// listPolicies lists IAM policies at the configured scope, following pagination.
func (s *Source) listPolicies(ctx context.Context) ([]iamItem, error) {
	var out []iamItem
	marker := ""
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		q := url.Values{"Action": {"ListPolicies"}, "Version": {iamAPIVersion}, "Scope": {s.cfg.policyScope}}
		if marker != "" {
			q.Set("Marker", marker)
		}
		var env policiesEnvelope
		if err := s.iamCall(ctx, q, &env); err != nil {
			return nil, err
		}
		for _, m := range env.Result.Policies {
			out = append(out, iamItem{name: m.name(), arn: m.Arn})
		}
		if !env.Result.IsTruncated || env.Result.Marker == "" {
			return out, nil
		}
		marker = env.Result.Marker
	}
}

// listAttachedRolePolicies lists the managed policies attached to one role,
// following pagination. It reads only attachment METADATA (policy name + ARN),
// never the policy document.
func (s *Source) listAttachedRolePolicies(ctx context.Context, role string) ([]iamItem, error) {
	var out []iamItem
	marker := ""
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		q := url.Values{"Action": {"ListAttachedRolePolicies"}, "Version": {iamAPIVersion}, "RoleName": {role}}
		if marker != "" {
			q.Set("Marker", marker)
		}
		var env attachedPoliciesEnvelope
		if err := s.iamCall(ctx, q, &env); err != nil {
			return nil, err
		}
		for _, m := range env.Result.AttachedPolicies {
			out = append(out, iamItem{name: m.PolicyName, arn: m.PolicyArn})
		}
		if !env.Result.IsTruncated || env.Result.Marker == "" {
			return out, nil
		}
		marker = env.Result.Marker
	}
}

// iamCall issues one SigV4-signed IAM Query request and decodes the XML response
// into out. The Query protocol places the action in the URL query string of a GET
// (a read), which AWS canonicalizes; the request carries no body. IAM is signed
// for the global us-east-1 region regardless of the operating region.
func (s *Source) iamCall(ctx context.Context, q url.Values, out any) error {
	endpoint := strings.TrimRight(s.cfg.iamEndpoint, "/") + "/?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	sign(req, nil, iamSigningService, iamSigningRegion, s.cfg.creds, time.Now())

	resp, err := s.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("iam: %s returned status %d", q.Get("Action"), resp.StatusCode)
	}
	if err := xml.Unmarshal(body, out); err != nil {
		return fmt.Errorf("iam: decode %s response: %w", q.Get("Action"), err)
	}
	return nil
}

// sortItems sorts discovered IAM items by ARN then name for deterministic emit
// order, so golden tests are stable regardless of API page ordering.
func sortItems(items []iamItem) {
	sort.Slice(items, func(i, j int) bool {
		if items[i].arn != items[j].arn {
			return items[i].arn < items[j].arn
		}
		return items[i].name < items[j].name
	})
}

// emit is a tiny helper that forwards an observation, returning Emit's error so
// callers can treat it as fatal to the pass (per the SDK contract).
func emit(ctx context.Context, sink sdk.Sink, obs model.Observation) error {
	return sink.Emit(ctx, obs)
}
