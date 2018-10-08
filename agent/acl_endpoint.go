package agent

import (
	"fmt"
	"io/ioutil"
	"net/http"
	"strings"
	"time"

	"github.com/hashicorp/consul/acl"
	"github.com/hashicorp/consul/agent/structs"
)

// aclCreateResponse is used to wrap the ACL ID
type aclBootstrapResponse struct {
	ID string
	structs.ACLToken
}

// checkACLDisabled will return a standard response if ACLs are disabled. This
// returns true if they are disabled and we should not continue.
func (s *HTTPServer) checkACLDisabled(resp http.ResponseWriter, req *http.Request) bool {
	if s.agent.delegate.ACLsEnabled() {
		return false
	}

	resp.WriteHeader(http.StatusUnauthorized)
	fmt.Fprint(resp, "ACL support disabled")
	return true
}

// ACLBootstrap is used to perform a one-time ACL bootstrap operation on
// a cluster to get the first management token.
func (s *HTTPServer) ACLBootstrap(resp http.ResponseWriter, req *http.Request) (interface{}, error) {
	if s.checkACLDisabled(resp, req) {
		return nil, nil
	}

	args := structs.DCSpecificRequest{
		Datacenter: s.agent.config.Datacenter,
	}

	var out structs.ACLToken
	err := s.agent.RPC("ACL.Bootstrap", &args, &out)
	if err != nil {
		if strings.Contains(err.Error(), structs.ACLBootstrapNotAllowedErr.Error()) {
			resp.WriteHeader(http.StatusForbidden)
			fmt.Fprint(resp, acl.PermissionDeniedError{Cause: err.Error()}.Error())
			return nil, nil
		} else {
			return nil, err
		}
	}

	return &aclBootstrapResponse{ID: out.SecretID, ACLToken: out}, nil
}

func (s *HTTPServer) ACLReplicationStatus(resp http.ResponseWriter, req *http.Request) (interface{}, error) {
	if s.checkACLDisabled(resp, req) {
		return nil, nil
	}

	// Note that we do not forward to the ACL DC here. This is a query for
	// any DC that's doing replication.
	args := structs.DCSpecificRequest{}
	s.parseSource(req, &args.Source)
	if done := s.parse(resp, req, &args.Datacenter, &args.QueryOptions); done {
		return nil, nil
	}

	// Make the request.
	var out structs.ACLReplicationStatus
	if err := s.agent.RPC("ACL.ReplicationStatus", &args, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *HTTPServer) ACLRulesTranslate(resp http.ResponseWriter, req *http.Request) (interface{}, error) {
	policyBytes, err := ioutil.ReadAll(req.Body)
	if err != nil {
		return nil, BadRequestError{Reason: fmt.Sprintf("Failed to read body: %v", err)}
	}

	translated, err := acl.TranslateLegacyRules(policyBytes)
	if err != nil {
		return nil, BadRequestError{Reason: err.Error()}
	}

	resp.Write(translated)
	return nil, nil
}

func (s *HTTPServer) ACLRulesTranslateLegacyToken(resp http.ResponseWriter, req *http.Request) (interface{}, error) {
	tokenID := strings.TrimPrefix(req.URL.Path, "/v1/acl/rules/translate/")
	if tokenID == "" {
		return nil, BadRequestError{Reason: "Missing token ID"}
	}

	args := structs.ACLTokenReadRequest{
		Datacenter:  s.agent.config.Datacenter,
		TokenID:     tokenID,
		TokenIDType: structs.ACLTokenAccessor,
	}
	if done := s.parse(resp, req, &args.Datacenter, &args.QueryOptions); done {
		return nil, nil
	}

	if args.Datacenter == "" {
		args.Datacenter = s.agent.config.Datacenter
	}

	args.QueryOptions.MinQueryIndex = 0

	var out structs.ACLTokenResponse
	defer setMeta(resp, &out.QueryMeta)
	if err := s.agent.RPC("ACL.TokenRead", &args, &out); err != nil {
		return nil, err
	}

	if out.Token == nil {
		return nil, acl.ErrNotFound
	}

	if out.Token.Rules == "" {
		return nil, fmt.Errorf("The specified token does not have any rules set")
	}

	translated, err := acl.TranslateLegacyRules([]byte(out.Token.Rules))
	if err != nil {
		return nil, fmt.Errorf("Failed to parse legacy rules: %v", err)
	}

	resp.Write(translated)
	return nil, nil
}

func (s *HTTPServer) ACLPolicyList(resp http.ResponseWriter, req *http.Request) (interface{}, error) {
	if s.checkACLDisabled(resp, req) {
		return nil, nil
	}

	var args structs.DCSpecificRequest
	if done := s.parse(resp, req, &args.Datacenter, &args.QueryOptions); done {
		return nil, nil
	}

	if args.Datacenter == "" {
		args.Datacenter = s.agent.config.Datacenter
	}

	var out structs.ACLPolicyMultiResponse
	defer setMeta(resp, &out.QueryMeta)
	if err := s.agent.RPC("ACL.PolicyList", &args, &out); err != nil {
		return nil, err
	}

	stubs := make([]*structs.ACLPolicyListStub, 0, len(out.Policies))
	for _, policy := range out.Policies {
		stubs = append(stubs, policy.Stub())
	}

	return out.Policies, nil
}

func (s *HTTPServer) ACLPolicyCRUD(resp http.ResponseWriter, req *http.Request) (interface{}, error) {
	if s.checkACLDisabled(resp, req) {
		return nil, nil
	}

	var fn func(resp http.ResponseWriter, req *http.Request, policyID string, policyIDType structs.ACLPolicyIDType) (interface{}, error)

	switch req.Method {
	case "GET":
		fn = s.ACLPolicyRead

	case "PUT":
		fn = s.ACLPolicyWrite

	case "DELETE":
		fn = s.ACLPolicyDelete

	default:
		return nil, MethodNotAllowedError{req.Method, []string{"GET", "PUT", "DELETE"}}
	}

	policyID := strings.TrimPrefix(req.URL.Path, "/v1/acl/policy/")
	if policyID == "" && req.Method != "PUT" {
		return nil, BadRequestError{Reason: "Missing policy ID"}
	}

	policyIDType := structs.ACLPolicyID

	if idType := req.URL.Query().Get("idType"); idType != "" {
		switch idType {
		case "id":
			policyIDType = structs.ACLPolicyID
		case "name":
			policyIDType = structs.ACLPolicyName
		default:
			return nil, BadRequestError{Reason: "Invalid value for idType parameter"}
		}
	}

	return fn(resp, req, policyID, policyIDType)
}

func (s *HTTPServer) ACLPolicyRead(resp http.ResponseWriter, req *http.Request, policyID string, policyIDType structs.ACLPolicyIDType) (interface{}, error) {
	args := structs.ACLPolicyReadRequest{
		Datacenter:   s.agent.config.Datacenter,
		PolicyID:     policyID,
		PolicyIDType: policyIDType,
	}
	if done := s.parse(resp, req, &args.Datacenter, &args.QueryOptions); done {
		return nil, nil
	}

	if args.Datacenter == "" {
		args.Datacenter = s.agent.config.Datacenter
	}

	var out structs.ACLPolicyResponse
	defer setMeta(resp, &out.QueryMeta)
	if err := s.agent.RPC("ACL.PolicyRead", &args, &out); err != nil {
		return nil, err
	}

	if out.Policy == nil {
		return nil, acl.ErrNotFound
	}

	return out.Policy, nil
}

func (s *HTTPServer) ACLPolicyCreate(resp http.ResponseWriter, req *http.Request) (interface{}, error) {
	return s.ACLPolicyWrite(resp, req, "", structs.ACLPolicyID)
}

// fixCreateTime is used to help in decoding the CreateTime attribute from
// both the ACL Token and ACL Policy creation/update requests. It is needed
// to help mapstructure decode things properly when decodeBody is used.
func fixCreateTime(raw interface{}) error {
	rawMap, ok := raw.(map[string]interface{})
	if !ok {
		return nil
	}

	if val, ok := rawMap["CreateTime"]; ok {
		if sval, ok := val.(string); ok {
			t, err := time.Parse(time.RFC3339, sval)
			if err != nil {
				return err
			}
			rawMap["CreateTime"] = t
		}
	}
	return nil
}

func (s *HTTPServer) ACLPolicyWrite(resp http.ResponseWriter, req *http.Request, policyID string, policyIDType structs.ACLPolicyIDType) (interface{}, error) {
	args := structs.ACLPolicyUpsertRequest{
		Datacenter: s.agent.config.Datacenter,
	}
	s.parseToken(req, &args.Token)

	if err := decodeBody(req, &args.Policy, nil); err != nil {
		return nil, BadRequestError{Reason: fmt.Sprintf("Policy decoding failed: %v", err)}
	}

	args.Policy.Syntax = acl.SyntaxCurrent

	// TODO (ACL-V2) - Should we allow not specifying the ID in the payload when its specified in the URL
	switch policyIDType {
	case structs.ACLPolicyID:
		if policyID != "" && args.Policy.ID != "" && args.Policy.ID != policyID {
			return nil, BadRequestError{Reason: "Policy ID in URL and payload do not match"}
		} else if args.Policy.ID == "" {
			args.Policy.ID = policyID
		}
	case structs.ACLPolicyName:
		if policyID != "" && args.Policy.Name != "" && args.Policy.Name != policyID {
			return nil, BadRequestError{Reason: "Policy Name in URL and payload do not match"}
		} else if args.Policy.Name == "" {
			args.Policy.Name = ""
		}
	}

	var out structs.ACLPolicy
	if err := s.agent.RPC("ACL.PolicyUpsert", args, &out); err != nil {
		return nil, err
	}

	return &out, nil
}

func (s *HTTPServer) ACLPolicyDelete(resp http.ResponseWriter, req *http.Request, policyID string, policyIDType structs.ACLPolicyIDType) (interface{}, error) {
	args := structs.ACLPolicyDeleteRequest{
		Datacenter:   s.agent.config.Datacenter,
		PolicyID:     policyID,
		PolicyIDType: policyIDType,
	}
	s.parseToken(req, &args.Token)

	var out string
	if err := s.agent.RPC("ACL.PolicyDelete", args, &out); err != nil {
		return nil, err
	}

	return true, nil
}

func (s *HTTPServer) ACLTokenList(resp http.ResponseWriter, req *http.Request) (interface{}, error) {
	if s.checkACLDisabled(resp, req) {
		return nil, nil
	}

	args := &structs.ACLTokenListRequest{
		IncludeLocal:  true,
		IncludeGlobal: true,
	}
	if done := s.parse(resp, req, &args.Datacenter, &args.QueryOptions); done {
		return nil, nil
	}

	if args.Datacenter == "" {
		args.Datacenter = s.agent.config.Datacenter
	}

	args.Policy = req.URL.Query().Get("policy")

	var out structs.ACLTokensResponse
	defer setMeta(resp, &out.QueryMeta)
	if err := s.agent.RPC("ACL.TokenList", &args, &out); err != nil {
		return nil, err
	}

	stubs := make([]*structs.ACLTokenListStub, 0, len(out.Tokens))
	for _, token := range out.Tokens {
		stubs = append(stubs, token.Stub())
	}

	return stubs, nil
}

func (s *HTTPServer) ACLTokenCRUD(resp http.ResponseWriter, req *http.Request) (interface{}, error) {
	if s.checkACLDisabled(resp, req) {
		return nil, nil
	}

	var fn func(resp http.ResponseWriter, req *http.Request, tokenID string) (interface{}, error)

	switch req.Method {
	case "GET":
		fn = s.ACLTokenRead

	case "PUT":
		fn = s.ACLTokenWrite

	case "DELETE":
		fn = s.ACLTokenDelete

	default:
		return nil, MethodNotAllowedError{req.Method, []string{"GET", "PUT", "DELETE"}}
	}

	tokenID := strings.TrimPrefix(req.URL.Path, "/v1/acl/token/")
	if tokenID == "" && req.Method != "PUT" {
		return nil, BadRequestError{Reason: "Missing token ID"}
	}

	return fn(resp, req, tokenID)
}

func (s *HTTPServer) ACLTokenSelf(resp http.ResponseWriter, req *http.Request) (interface{}, error) {
	args := structs.ACLTokenReadRequest{
		TokenIDType: structs.ACLTokenSecret,
	}

	if done := s.parse(resp, req, &args.Datacenter, &args.QueryOptions); done {
		return nil, nil
	}

	// copy the token parameter to the ID
	args.TokenID = args.Token

	if args.Datacenter == "" {
		args.Datacenter = s.agent.config.Datacenter
	}

	var out structs.ACLTokenResponse
	defer setMeta(resp, &out.QueryMeta)
	if err := s.agent.RPC("ACL.TokenRead", &args, &out); err != nil {
		return nil, err
	}

	if out.Token == nil {
		return nil, acl.ErrNotFound
	}

	return out.Token, nil
}

func (s *HTTPServer) ACLTokenCreate(resp http.ResponseWriter, req *http.Request) (interface{}, error) {
	if s.checkACLDisabled(resp, req) {
		return nil, nil
	}

	return s.ACLTokenWrite(resp, req, "")
}

func (s *HTTPServer) ACLTokenRead(resp http.ResponseWriter, req *http.Request, tokenID string) (interface{}, error) {
	args := structs.ACLTokenReadRequest{
		Datacenter:  s.agent.config.Datacenter,
		TokenID:     tokenID,
		TokenIDType: structs.ACLTokenAccessor,
	}

	if done := s.parse(resp, req, &args.Datacenter, &args.QueryOptions); done {
		return nil, nil
	}

	if args.Datacenter == "" {
		args.Datacenter = s.agent.config.Datacenter
	}

	var out structs.ACLTokenResponse
	defer setMeta(resp, &out.QueryMeta)
	if err := s.agent.RPC("ACL.TokenRead", &args, &out); err != nil {
		return nil, err
	}

	if out.Token == nil {
		return nil, acl.ErrNotFound
	}

	return out.Token, nil
}

func (s *HTTPServer) ACLTokenWrite(resp http.ResponseWriter, req *http.Request, tokenID string) (interface{}, error) {
	args := structs.ACLTokenUpsertRequest{
		Datacenter: s.agent.config.Datacenter,
	}
	s.parseToken(req, &args.Token)

	if err := decodeBody(req, &args.ACLToken, fixCreateTime); err != nil {
		return nil, BadRequestError{Reason: fmt.Sprintf("Token decoding failed: %v", err)}
	}

	if args.ACLToken.AccessorID == "" {
		args.ACLToken.AccessorID = tokenID
	} else if tokenID != "" && args.ACLToken.AccessorID != tokenID {
		return nil, BadRequestError{Reason: "Token Accessor ID in URL and payload do not match"}
	}

	var out structs.ACLToken
	if err := s.agent.RPC("ACL.TokenUpsert", args, &out); err != nil {
		return nil, err
	}

	return &out, nil
}

func (s *HTTPServer) ACLTokenDelete(resp http.ResponseWriter, req *http.Request, tokenID string) (interface{}, error) {
	args := structs.ACLTokenDeleteRequest{
		Datacenter: s.agent.config.Datacenter,
		TokenID:    tokenID,
	}
	s.parseToken(req, &args.Token)

	var out string
	if err := s.agent.RPC("ACL.TokenDelete", args, &out); err != nil {
		return nil, err
	}
	return true, nil
}

func (s *HTTPServer) ACLTokenClone(resp http.ResponseWriter, req *http.Request) (interface{}, error) {
	if s.checkACLDisabled(resp, req) {
		return nil, nil
	}

	tokenID := strings.TrimPrefix(req.URL.Path, "/v1/acl/token/clone/")
	if tokenID == "" {
		return nil, BadRequestError{Reason: "Missing token ID"}
	}

	args := structs.ACLTokenUpsertRequest{
		Datacenter: s.agent.config.Datacenter,
	}

	if err := decodeBody(req, &args.ACLToken, fixCreateTime); err != nil && err.Error() != "EOF" {
		return nil, BadRequestError{Reason: fmt.Sprintf("Token decoding failed: %v", err)}
	}
	s.parseToken(req, &args.Token)

	// Set this for the ID to clone
	args.ACLToken.AccessorID = tokenID

	var out structs.ACLToken
	if err := s.agent.RPC("ACL.TokenClone", args, &out); err != nil {
		return nil, err
	}

	return &out, nil
}

func (s *HTTPServer) ACLTokenUpgrade(resp http.ResponseWriter, req *http.Request) (interface{}, error) {
	if s.checkACLDisabled(resp, req) {
		return nil, nil
	}

	args := structs.ACLTokenUpsertRequest{
		Datacenter: s.agent.config.Datacenter,
	}
	s.parseToken(req, &args.Token)

	tokenID := strings.TrimPrefix(req.URL.Path, "/v1/acl/token/upgrade/")
	if tokenID == "" {
		return nil, BadRequestError{Reason: "Missing token ID"}
	}

	if err := decodeBody(req, &args.ACLToken, fixCreateTime); err != nil {
		return nil, BadRequestError{Reason: fmt.Sprintf("Token decoding failed: %v", err)}
	}

	if args.ACLToken.AccessorID == "" {
		args.ACLToken.AccessorID = tokenID
	} else if tokenID != "" && args.ACLToken.AccessorID != tokenID {
		return nil, BadRequestError{Reason: "Token Accessor ID in URL and payload do not match"}
	}

	var out structs.ACLToken
	if err := s.agent.RPC("ACL.TokenUpgrade", args, &out); err != nil {
		return nil, err
	}

	return &out, nil
}
