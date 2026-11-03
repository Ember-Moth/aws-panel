package service

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Ember-Moth/aws-panel/internal/auth"
	"github.com/Ember-Moth/aws-panel/internal/infra/clock"
	ac "github.com/Ember-Moth/aws-panel/internal/module/accounts/contract"
	"github.com/Ember-Moth/aws-panel/internal/module/inventory/contract"
	"github.com/Ember-Moth/aws-panel/internal/module/inventory/entity"
	"github.com/Ember-Moth/aws-panel/internal/module/inventory/internal/executor"
	"github.com/Ember-Moth/aws-panel/internal/module/inventory/internal/scope"
	oc "github.com/Ember-Moth/aws-panel/internal/module/operations/contract"
	"github.com/Ember-Moth/aws-panel/internal/repository"
	"github.com/google/uuid"
)

type Accounts interface {
	ListAccounts(context.Context, int64) ([]ac.Account, error)
	AccountDetail(context.Context, int64, string) (*ac.AccountDetail, error)
	Inspect(context.Context, int64, string, string, bool) (ac.Snapshot, error)
}
type Access interface {
	Principal(context.Context, int64) (auth.Principal, error)
}
type Jobs interface {
	Prepare(context.Context, int64, oc.PlanRequest) (oc.Plan, error)
	Submit(context.Context, int64, oc.SubmitRequest, auth.RequestMeta) (oc.Job, error)
	Submission(context.Context, int64, string, string, int, json.RawMessage) (oc.Job, error)
}
type Observer interface {
	LatestTargets(context.Context, string, []oc.TargetRef) ([]oc.TargetState, error)
}
type Deps struct {
	Store    repository.InventoryStore
	Accounts Accounts
	Access   Access
	Jobs     Jobs
	Observer Observer
}
type Service struct{ deps Deps }

func New(deps Deps) *Service  { return &Service{deps: deps} }
func validID(id string) bool  { _, err := uuid.Parse(id); return err == nil }
func encode(value any) string { raw, _ := json.Marshal(value); return string(raw) }
func textOK(value string, max int) bool {
	return strings.TrimSpace(value) != "" && utf8.ValidString(value) && utf8.RuneCountInString(value) <= max && strings.IndexFunc(value, unicode.IsControl) < 0
}
func (s *Service) actor(ctx context.Context, id int64) (auth.Principal, error) {
	p, err := s.deps.Access.Principal(ctx, id)
	if errors.Is(err, auth.ErrDenied) {
		return p, contract.ErrForbidden
	}
	if err != nil {
		return p, contract.ErrUnavailable
	}
	return p, nil
}
func translate(err error) error {
	var op *oc.Error
	if errors.As(err, &op) {
		switch op.Code {
		case oc.ErrForbidden.Code:
			return contract.ErrForbidden
		case oc.ErrInvalid.Code:
			return contract.ErrInvalid
		case oc.ErrNotFound.Code:
			return contract.ErrNotFound
		case oc.ErrConflict.Code:
			return contract.ErrConflict
		case oc.ErrBinding.Code:
			return &contract.Error{Code: op.Code, Message: "访问连接已变更或不可用，请先检查并重新验证"}
		default:
			return &contract.Error{Code: op.Code, Message: op.Message}
		}
	}
	var account *ac.Error
	if errors.As(err, &account) {
		switch account.Code {
		case ac.ErrForbidden.Code:
			return contract.ErrForbidden
		case ac.ErrNotFound.Code:
			return contract.ErrNotFound
		default:
			return &contract.Error{Code: account.Code, Message: account.Message}
		}
	}
	return err
}
func (s *Service) accounts(ctx context.Context, actorID int64) ([]ac.Account, map[string]string, error) {
	if _, err := s.actor(ctx, actorID); err != nil {
		return nil, nil, err
	}
	rows, err := s.deps.Accounts.ListAccounts(ctx, actorID)
	if err != nil {
		return nil, nil, translate(err)
	}
	names := map[string]string{}
	for _, row := range rows {
		names[row.ID] = row.Name
	}
	return rows, names, nil
}
func ids(rows []ac.Account) []string {
	result := make([]string, 0, len(rows))
	for _, row := range rows {
		result = append(result, row.ID)
	}
	return result
}
func (s *Service) Options(ctx context.Context, actorID int64) (contract.Options, error) {
	rows, _, err := s.accounts(ctx, actorID)
	if err != nil {
		return contract.Options{}, err
	}
	result := contract.Options{Accounts: []contract.AccountChoice{}, Regions: []string{}}
	for _, row := range rows {
		account := contract.AccountChoice{ID: row.ID, Name: row.Name, AWSAccountID: row.AWSAccountID, Partition: row.Partition, Connections: []contract.ConnectionChoice{}}
		detail, err := s.deps.Accounts.AccountDetail(ctx, actorID, row.ID)
		if err != nil {
			return contract.Options{}, translate(err)
		}
		for _, connection := range detail.Connections {
			account.Connections = append(account.Connections, contract.ConnectionChoice{ID: connection.ID, Name: connection.Name, Status: connection.Status})
		}
		result.Accounts = append(result.Accounts, account)
	}
	sort.Slice(result.Accounts, func(i, j int) bool { return result.Accounts[i].Name < result.Accounts[j].Name })
	result.Regions, err = s.deps.Store.KnownRegions(ctx, ids(rows))
	if err != nil {
		return contract.Options{}, err
	}
	return result, nil
}
func (s *Service) query(ctx context.Context, actorID int64, filter contract.Filter) (entity.Query, map[string]string, error) {
	rows, names, err := s.accounts(ctx, actorID)
	if err != nil {
		return entity.Query{}, nil, err
	}
	if filter.Page < 1 || filter.Page > 10000 || filter.Kind != "" && !scope.KindOK(filter.Kind) || filter.Region != "" && !scope.RegionOK(filter.Region) || len(filter.State) > 80 || len(filter.Query) > 160 {
		return entity.Query{}, nil, contract.ErrInvalid
	}
	if !utf8.ValidString(filter.State) || !utf8.ValidString(filter.Query) || strings.ContainsRune(filter.State, 0) || strings.ContainsRune(filter.Query, 0) {
		return entity.Query{}, nil, contract.ErrInvalid
	}
	allowed := ids(rows)
	if filter.AccountID != "" {
		if !validID(filter.AccountID) {
			return entity.Query{}, nil, contract.ErrInvalid
		}
		if _, ok := names[filter.AccountID]; !ok {
			return entity.Query{}, nil, contract.ErrForbidden
		}
		allowed = []string{filter.AccountID}
	}
	return entity.Query{AccountIDs: allowed, Kind: filter.Kind, Region: filter.Region, State: filter.State, Text: filter.Query, IncludeMissing: filter.IncludeMissing, Page: filter.Page, Limit: 25}, names, nil
}
func timeView(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	v := clock.In(*value)
	return &v
}
func (s *Service) scopeViews(ctx context.Context, rows []entity.Scope, names map[string]string) ([]contract.Scope, error) {
	refs := make([]oc.TargetRef, 0, len(rows))
	for _, row := range rows {
		refs = append(refs, oc.TargetRef{AccountID: row.AccountID, TargetKey: scope.Key(row.AccountID, row.Kind, row.Region)})
	}
	states, err := s.deps.Observer.LatestTargets(ctx, scope.Operation, refs)
	if err != nil {
		return nil, translate(err)
	}
	latest := map[string]oc.TargetState{}
	for _, state := range states {
		latest[state.TargetKey] = state
	}
	result := make([]contract.Scope, 0, len(rows))
	for _, row := range rows {
		value := contract.Scope{ID: row.ID, AccountID: row.AccountID, AccountName: names[row.AccountID], Kind: row.Kind, Region: row.Region, Status: row.Status, Code: row.LastErrorCode, ResourceCount: row.ResourceCount, LastAttemptAt: timeView(row.LastAttemptAt), LastSuccessAt: timeView(row.LastSuccessAt)}
		if state, ok := latest[scope.Key(row.AccountID, row.Kind, row.Region)]; ok {
			value.LastJobID = state.JobID
			value.UpdatedAt = timeView(&state.UpdatedAt)
			switch state.Status {
			case oc.Queued, oc.Running, oc.Waiting:
				value.Status = "syncing"
				value.Code = state.Code
			case oc.Failed, oc.Attention:
				value.Status = "failed"
				value.Code = state.Code
			case oc.Cancelled:
				value.Status = "cancelled"
				value.Code = state.Code
			}
		}
		value.Stale = value.LastSuccessAt == nil || time.Since(*value.LastSuccessAt) > 30*time.Minute || value.Status == "failed" || value.Status == "cancelled"
		if value.Status != "ready" && value.Code != "" {
			value.Message = executor.Message(value.Code)
		}
		result = append(result, value)
	}
	return result, nil
}
func resourceView(row entity.Resource, names map[string]string) (contract.Resource, error) {
	var details contract.Details
	if json.Unmarshal([]byte(row.Details), &details) != nil {
		return contract.Resource{}, contract.ErrUnavailable
	}
	if details.EC2 != nil {
		details.EC2.CreatedAt = clock.In(details.EC2.CreatedAt)
	}
	if details.Instance != nil {
		details.Instance.CreatedAt = timeView(details.Instance.CreatedAt)
	}
	return contract.Resource{ID: row.ID, ScopeID: row.ScopeID, AccountID: row.AccountID, AccountName: names[row.AccountID], Kind: row.Kind, ResourceID: row.ResourceID, Name: row.Name, Region: row.Region, State: row.State, Details: details, ObservedAt: clock.In(row.ObservedAt), Present: row.Present, MissingSince: timeView(row.MissingSince)}, nil
}
func (s *Service) Resources(ctx context.Context, actorID int64, filter contract.Filter) (contract.ResourceList, error) {
	q, names, err := s.query(ctx, actorID, filter)
	if err != nil {
		return contract.ResourceList{}, err
	}
	rows, err := s.deps.Store.Resources(ctx, q)
	if err != nil {
		return contract.ResourceList{}, err
	}
	result := contract.ResourceList{Items: []contract.Resource{}, Scopes: []contract.Scope{}, Page: filter.Page}
	if filter.Page > 1 {
		result.Previous = filter.Page - 1
	}
	if len(rows) > q.Limit {
		result.Next = filter.Page + 1
		rows = rows[:q.Limit]
	}
	scopeIDs := map[string]bool{}
	scopeRows := []entity.Scope{}
	for _, row := range rows {
		value, err := resourceView(row, names)
		if err != nil {
			return contract.ResourceList{}, err
		}
		result.Items = append(result.Items, value)
		if !scopeIDs[row.ScopeID] {
			value, err := s.deps.Store.Scope(ctx, row.ScopeID)
			if err != nil {
				return contract.ResourceList{}, err
			}
			scopeRows = append(scopeRows, value)
			scopeIDs[row.ScopeID] = true
		}
	}
	if len(scopeRows) == 0 {
		q.Page = 1
		q.Limit = 25
		scopeRows, err = s.deps.Store.Scopes(ctx, q)
		if err != nil {
			return contract.ResourceList{}, err
		}
		if len(scopeRows) > 25 {
			scopeRows = scopeRows[:25]
		}
	}
	result.Scopes, err = s.scopeViews(ctx, scopeRows, names)
	return result, err
}
func (s *Service) Scopes(ctx context.Context, actorID int64, filter contract.Filter) (contract.ScopeList, error) {
	q, names, err := s.query(ctx, actorID, filter)
	if err != nil {
		return contract.ScopeList{}, err
	}
	rows, err := s.deps.Store.Scopes(ctx, q)
	if err != nil {
		return contract.ScopeList{}, err
	}
	result := contract.ScopeList{Page: filter.Page}
	if filter.Page > 1 {
		result.Previous = filter.Page - 1
	}
	if len(rows) > q.Limit {
		result.Next = filter.Page + 1
		rows = rows[:q.Limit]
	}
	result.Items, err = s.scopeViews(ctx, rows, names)
	return result, err
}
func (s *Service) Resource(ctx context.Context, actorID int64, id string) (contract.ResourceDetail, error) {
	_, names, err := s.accounts(ctx, actorID)
	if err != nil {
		return contract.ResourceDetail{}, err
	}
	if !validID(id) {
		return contract.ResourceDetail{}, contract.ErrInvalid
	}
	row, err := s.deps.Store.Resource(ctx, id)
	if err != nil {
		return contract.ResourceDetail{}, err
	}
	if _, ok := names[row.AccountID]; !ok {
		return contract.ResourceDetail{}, contract.ErrForbidden
	}
	value, err := resourceView(row, names)
	if err != nil {
		return contract.ResourceDetail{}, err
	}
	scopeRow, err := s.deps.Store.Scope(ctx, row.ScopeID)
	if err != nil {
		return contract.ResourceDetail{}, err
	}
	scopes, err := s.scopeViews(ctx, []entity.Scope{scopeRow}, names)
	if err != nil {
		return contract.ResourceDetail{}, err
	}
	return contract.ResourceDetail{Resource: value, Scope: scopes[0]}, nil
}

var keyPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{16,128}$`)

func (s *Service) prepare(ctx context.Context, actorID int64, targets []contract.RefreshTarget) (oc.Plan, error) {
	input := contract.RefreshInput{Targets: targets}
	raw, _ := json.Marshal(input)
	planned := make([]oc.Target, 0, len(targets))
	for _, target := range targets {
		planned = append(planned, oc.Target{Key: scope.InputKey(target), Label: scope.Label(target.Kind), Binding: oc.Binding{ConnectionID: target.ConnectionID, Region: target.Region}})
	}
	plan, err := s.deps.Jobs.Prepare(ctx, actorID, oc.PlanRequest{Operation: scope.Operation, InputVersion: 1, Input: raw, Targets: planned})
	return plan, translate(err)
}
func (s *Service) Refresh(ctx context.Context, actorID int64, req contract.RefreshRequest, meta auth.RequestMeta) (oc.Job, error) {
	if !keyPattern.MatchString(req.IdempotencyKey) {
		return oc.Job{}, contract.ErrInvalid
	}
	targets, err := scope.Normalize(req.Targets)
	if err != nil {
		return oc.Job{}, err
	}
	raw, _ := json.Marshal(contract.RefreshInput{Targets: targets})
	existing, err := s.deps.Jobs.Submission(ctx, actorID, scope.Operation, req.IdempotencyKey, 1, raw)
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, oc.ErrNotFound) {
		return oc.Job{}, translate(err)
	}
	plan, err := s.prepare(ctx, actorID, targets)
	if err != nil {
		return oc.Job{}, err
	}
	scopes := make([]entity.Scope, 0, len(plan.Items))
	for _, item := range plan.Items {
		var request contract.RefreshTarget
		if len(item.Steps) != 1 || json.Unmarshal(item.Steps[0].Input, &request) != nil {
			return oc.Job{}, contract.ErrUnavailable
		}
		scopes = append(scopes, entity.Scope{AccountID: item.Target.Binding.AccountID, Kind: request.Kind, Region: scope.Region(request.Kind, request.Region)})
	}
	if err := s.deps.Store.EnsureScopes(ctx, scopes); err != nil {
		return oc.Job{}, err
	}
	job, err := s.deps.Jobs.Submit(ctx, actorID, oc.SubmitRequest{PlanID: plan.ID, IdempotencyKey: req.IdempotencyKey}, meta)
	return job, translate(err)
}
