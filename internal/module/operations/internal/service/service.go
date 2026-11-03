package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Ember-Moth/aws-panel/internal/auth"
	"github.com/Ember-Moth/aws-panel/internal/infra/clock"
	"github.com/Ember-Moth/aws-panel/internal/module/operations/contract"
	"github.com/Ember-Moth/aws-panel/internal/module/operations/entity"
	"github.com/Ember-Moth/aws-panel/internal/repository"
	"github.com/Ember-Moth/aws-panel/internal/repository/kernel"
	"github.com/google/uuid"
)

type Access interface {
	Principal(context.Context, int64) (auth.Principal, error)
	AccountAccess(context.Context, int64, string, bool) (bool, error)
	AccountScope(context.Context, int64) ([]string, error)
}
type Connections interface {
	Inspect(context.Context, int64, string, string, bool) (contract.Binding, error)
}
type Publisher interface {
	Publish(context.Context, string, contract.Message) error
}
type Deps struct {
	Store                          repository.OperationsStore
	Access                         Access
	Connections                    Connections
	Publisher                      Publisher
	Executors                      []contract.Executor
	LeaseDuration, RedeliveryAfter time.Duration
	CloudWritesEnabled             bool
}
type Service struct {
	deps      Deps
	executors map[string]contract.Executor
	current   map[string]contract.Executor
	owner     string
}

func key(id string, version int) string { return fmt.Sprintf("%s/%d", id, version) }

var identifier = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,79}$`)
var codePattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,79}$`)

func New(deps Deps) (*Service, error) {
	if deps.Store == nil || deps.Access == nil {
		return nil, contract.ErrInvalid
	}
	if deps.LeaseDuration <= 0 {
		deps.LeaseDuration = 30 * time.Second
	}
	if deps.RedeliveryAfter <= 0 {
		deps.RedeliveryAfter = 30 * time.Second
	}
	if deps.LeaseDuration < 150*time.Millisecond || deps.RedeliveryAfter < 100*time.Millisecond {
		return nil, contract.ErrInvalid
	}
	s := &Service{deps: deps, executors: map[string]contract.Executor{}, current: map[string]contract.Executor{}, owner: uuid.NewString()}
	for _, executor := range deps.Executors {
		d := executor.Definition()
		if !identifier.MatchString(d.ID) || d.Version < 1 || d.InputVersion < 1 || !labelOK(d.Title) || d.MaxAttempts < 1 || d.MaxAttempts > 100 || d.Deadline < time.Second || d.Deadline > 24*time.Hour || d.CallTimeout < 100*time.Millisecond || d.CallTimeout > 2*time.Minute {
			return nil, contract.ErrInvalid
		}
		if s.executors[key(d.ID, d.Version)] != nil {
			return nil, contract.ErrConflict
		}
		s.executors[key(d.ID, d.Version)] = executor
		if old := s.current[d.ID]; old == nil || old.Definition().Version < d.Version {
			s.current[d.ID] = executor
		}
	}
	return s, nil
}
func validID(id string) bool { _, err := uuid.Parse(id); return err == nil }
func labelOK(value string) bool {
	return utf8.ValidString(value) && strings.TrimSpace(value) != "" && utf8.RuneCountInString(value) <= 160 && strings.IndexFunc(value, unicode.IsControl) < 0
}
func encode(value any) string { raw, _ := json.Marshal(value); return string(raw) }
func decode[T any](raw string) (T, error) {
	var value T
	err := json.Unmarshal([]byte(raw), &value)
	if err != nil {
		return value, contract.ErrUnavailable
	}
	return value, nil
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
func (s *Service) authorize(ctx context.Context, actor auth.Principal, d contract.Definition, binding contract.Binding, write bool) error {
	if d.Diagnostic {
		if actor.Role != "admin" {
			return contract.ErrForbidden
		}
		return nil
	}
	if !validID(binding.AccountID) || !validID(binding.ConnectionID) {
		return contract.ErrInvalid
	}
	ok, err := s.deps.Access.AccountAccess(ctx, actor.ID, binding.AccountID, write)
	if err != nil {
		return contract.ErrUnavailable
	}
	if !ok {
		return contract.ErrForbidden
	}
	return nil
}
func (s *Service) inspect(ctx context.Context, actorID int64, binding contract.Binding, write bool) (contract.Binding, error) {
	if s.deps.Connections == nil {
		return contract.Binding{}, contract.ErrUnavailable
	}
	current, err := s.deps.Connections.Inspect(ctx, actorID, binding.ConnectionID, binding.Region, write)
	if err != nil {
		return contract.Binding{}, err
	}
	if current.AccountID != binding.AccountID && binding.AccountID != "" {
		return contract.Binding{}, contract.ErrBinding
	}
	return current, nil
}
func sameBinding(a, b contract.Binding) bool {
	return a.AccountID == b.AccountID && a.ConnectionID == b.ConnectionID && a.AWSAccountID == b.AWSAccountID && a.Partition == b.Partition && a.Region == b.Region && a.Fingerprint == b.Fingerprint
}
func audit(meta auth.RequestMeta, actor auth.Principal, action, jobID string, at time.Time, details map[string]string) kernel.AuditEntry {
	return kernel.AuditEntry{ActorID: &actor.ID, ActorName: actor.Username, Action: action, TargetType: "job", TargetID: jobID, Outcome: "success", RequestID: meta.RequestID, ClientIP: meta.ClientIP, Metadata: details, OccurredAt: at}
}
func (s *Service) Prepare(ctx context.Context, actorID int64, req contract.PlanRequest) (contract.Plan, error) {
	actor, err := s.actor(ctx, actorID)
	if err != nil {
		return contract.Plan{}, err
	}
	executor := s.current[req.Operation]
	if executor == nil {
		return contract.Plan{}, contract.ErrExecutor
	}
	d := executor.Definition()
	if req.InputVersion != d.InputVersion || len(req.Input) > 64<<10 || len(req.Targets) > 50 {
		return contract.Plan{}, contract.ErrInvalid
	}
	if d.Diagnostic && actor.Role != "admin" {
		return contract.Plan{}, contract.ErrForbidden
	}
	input, err := executor.Validate(req.Input)
	if err != nil {
		return contract.Plan{}, err
	}
	if !json.Valid(input) || len(input) > 64<<10 {
		return contract.Plan{}, contract.ErrInvalid
	}
	targets := make([]contract.Target, len(req.Targets))
	copy(targets, req.Targets)
	for i := range targets {
		if d.Diagnostic {
			targets[i].Binding = contract.Binding{}
			continue
		}
		binding, err := s.inspect(ctx, actorID, targets[i].Binding, d.WritesCloud)
		if err != nil {
			return contract.Plan{}, err
		}
		if err := s.authorize(ctx, actor, d, binding, d.WritesCloud); err != nil {
			return contract.Plan{}, err
		}
		targets[i].Binding = binding
	}
	planning, cancel := context.WithTimeout(auth.WithPrincipal(ctx, actor, auth.CSRF(ctx)), d.CallTimeout)
	defer cancel()
	items, err := executor.Plan(planning, input, targets)
	if err != nil {
		return contract.Plan{}, err
	}
	if len(items) == 0 || len(items) > 50 {
		return contract.Plan{}, contract.ErrInvalid
	}
	seen := map[string]bool{}
	for _, item := range items {
		if len(item.Target.LockKeys) > 8 {
			return contract.Plan{}, contract.ErrInvalid
		}
		for _, lock := range item.Target.LockKeys {
			if !labelOK(lock) {
				return contract.Plan{}, contract.ErrInvalid
			}
		}
		if (!d.Diagnostic && d.WritesCloud && item.Target.LockKey == "") || (item.Target.LockKey != "" && !labelOK(item.Target.LockKey)) {
			return contract.Plan{}, contract.ErrInvalid
		}
		if !labelOK(item.Target.Key) || !labelOK(item.Target.Label) || seen[item.Target.Key] || len(item.Steps) == 0 || len(item.Steps) > 16 {
			return contract.Plan{}, contract.ErrInvalid
		}
		seen[item.Target.Key] = true
		if err := s.authorize(ctx, actor, d, item.Target.Binding, d.WritesCloud); err != nil {
			return contract.Plan{}, err
		}
		if !d.Diagnostic {
			current, err := s.inspect(ctx, actorID, item.Target.Binding, d.WritesCloud)
			if err != nil {
				return contract.Plan{}, err
			}
			if !sameBinding(current, item.Target.Binding) {
				return contract.Plan{}, contract.ErrBinding
			}
		}
		for _, step := range item.Steps {
			if !identifier.MatchString(step.Kind) || !labelOK(step.Label) || !json.Valid(step.Input) || len(step.Input) > 64<<10 || step.WritesCloud && !d.WritesCloud {
				return contract.Plan{}, contract.ErrInvalid
			}
		}
	}
	if len(encode(items)) > 512<<10 {
		return contract.Plan{}, contract.ErrInvalid
	}
	hash := sha256.Sum256([]byte(encode(struct {
		Operation             string
		Version, InputVersion int
		Input                 json.RawMessage
		Items                 []contract.ItemPlan
	}{d.ID, d.Version, d.InputVersion, input, items})))
	row := entity.Plan{ID: uuid.NewString(), ActorID: actorID, Operation: d.ID, ExecutorVersion: d.Version, InputVersion: d.InputVersion, Input: string(input), InputHash: hex.EncodeToString(hash[:]), Items: encode(items)}
	err = s.deps.Store.InOperationsTx(ctx, func(tx repository.OperationsTx) error {
		now, err := tx.Now(ctx)
		if err != nil {
			return err
		}
		row.CreatedAt = now
		row.ExpiresAt = now.Add(10 * time.Minute)
		return tx.CreatePlan(ctx, row)
	})
	return s.planView(row, d, items), err
}
func (s *Service) planView(row entity.Plan, definition contract.Definition, items []contract.ItemPlan) contract.Plan {
	return contract.Plan{ID: row.ID, Operation: row.Operation, Title: definition.Title, Diagnostic: definition.Diagnostic, WritesCloud: definition.WritesCloud, CanSubmit: !definition.WritesCloud || s.deps.CloudWritesEnabled, InputVersion: row.InputVersion, ExecutorVersion: row.ExecutorVersion, Items: items, CreatedAt: clock.In(row.CreatedAt), ExpiresAt: clock.In(row.ExpiresAt)}
}
func (s *Service) Plan(ctx context.Context, actorID int64, id string) (contract.Plan, error) {
	actor, err := s.actor(ctx, actorID)
	if err != nil {
		return contract.Plan{}, err
	}
	if !validID(id) {
		return contract.Plan{}, contract.ErrInvalid
	}
	row, err := s.deps.Store.Plan(ctx, id)
	if err != nil {
		return contract.Plan{}, err
	}
	if row.ActorID != actor.ID {
		return contract.Plan{}, contract.ErrForbidden
	}
	executor := s.executors[key(row.Operation, row.ExecutorVersion)]
	if executor == nil {
		return contract.Plan{}, contract.ErrExecutor
	}
	items, err := decode[[]contract.ItemPlan](row.Items)
	if err != nil {
		return contract.Plan{}, err
	}
	for _, item := range items {
		if err := s.authorize(ctx, actor, executor.Definition(), item.Target.Binding, false); err != nil {
			return contract.Plan{}, err
		}
	}
	return s.planView(row, executor.Definition(), items), nil
}
func jobView(row entity.Job) contract.Job {
	var completed *time.Time
	if row.CompletedAt != nil {
		v := clock.In(*row.CompletedAt)
		completed = &v
	}
	return contract.Job{ID: row.ID, Operation: row.Operation, Title: row.Title, ActorName: row.ActorName, Status: row.Status, Diagnostic: row.Diagnostic, CancelRequested: row.CancelRequested, ExecutorVersion: row.ExecutorVersion, InputVersion: row.InputVersion, Version: row.Version, EventSeq: row.EventSeq, CreatedAt: clock.In(row.CreatedAt), UpdatedAt: clock.In(row.UpdatedAt), CompletedAt: completed}
}
func terminal(status string) bool {
	return status == contract.Succeeded || status == contract.Failed || status == contract.Cancelled || status == contract.PartialSuccess
}
func (s *Service) readJob(ctx context.Context, actorID int64, id string, manage bool) (entity.Job, []entity.Item, auth.Principal, error) {
	actor, err := s.actor(ctx, actorID)
	if err != nil {
		return entity.Job{}, nil, actor, err
	}
	if !validID(id) {
		return entity.Job{}, nil, actor, contract.ErrInvalid
	}
	job, err := s.deps.Store.Job(ctx, id)
	if err != nil {
		return job, nil, actor, err
	}
	if manage && actor.Role != "admin" && job.ActorID != actor.ID {
		return job, nil, actor, contract.ErrForbidden
	}
	if manage && job.WritesCloud && actor.Role != "admin" && actor.Role != "operator" {
		return job, nil, actor, contract.ErrForbidden
	}
	items, err := s.deps.Store.Items(ctx, id)
	if err != nil {
		return job, nil, actor, err
	}
	d := contract.Definition{Diagnostic: job.Diagnostic}
	for _, item := range items {
		binding, err := decode[contract.Binding](item.Binding)
		if err != nil {
			return job, nil, actor, err
		}
		if err := s.authorize(ctx, actor, d, binding, false); err != nil {
			return job, nil, actor, err
		}
	}
	return job, items, actor, nil
}
func (s *Service) List(ctx context.Context, actorID int64, page int) (contract.JobList, error) {
	actor, err := s.actor(ctx, actorID)
	if err != nil {
		return contract.JobList{}, err
	}
	if page < 1 || page > 10000 {
		return contract.JobList{}, contract.ErrInvalid
	}
	scope := entity.Scope{Admin: actor.Role == "admin"}
	if !scope.Admin {
		scope.AccountIDs, err = s.deps.Access.AccountScope(ctx, actorID)
		if err != nil {
			return contract.JobList{}, contract.ErrUnavailable
		}
	}
	rows, err := s.deps.Store.Jobs(ctx, scope, page, 25)
	if err != nil {
		return contract.JobList{}, err
	}
	result := contract.JobList{Items: []contract.Job{}, Page: page}
	if page > 1 {
		result.Previous = page - 1
	}
	if len(rows) > 25 {
		result.Next = page + 1
		rows = rows[:25]
	}
	for _, row := range rows {
		result.Items = append(result.Items, jobView(row))
	}
	return result, nil
}
func timeView(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	v := clock.In(*value)
	return &v
}
func eventView(row entity.Event) contract.Event {
	return contract.Event{Seq: row.Seq, Type: row.Type, StepID: row.StepID, ItemID: row.ItemID, Code: row.Code, Message: row.Message, OccurredAt: clock.In(row.OccurredAt)}
}
func (s *Service) Detail(ctx context.Context, actorID int64, id string) (contract.Detail, error) {
	job, items, actor, err := s.readJob(ctx, actorID, id, false)
	if err != nil {
		return contract.Detail{}, err
	}
	steps, err := s.deps.Store.Steps(ctx, id)
	if err != nil {
		return contract.Detail{}, err
	}
	attempts, err := s.deps.Store.Attempts(ctx, id)
	if err != nil {
		return contract.Detail{}, err
	}
	events, err := s.deps.Store.Events(ctx, id, max(0, job.EventSeq-50), 50)
	if err != nil {
		return contract.Detail{}, err
	}
	result := contract.Detail{Job: jobView(job), Items: []contract.Item{}, Events: []contract.Event{}, CanManage: actor.Role == "admin" || actor.ID == job.ActorID}
	if job.WritesCloud && actor.Role != "admin" && actor.Role != "operator" {
		result.CanManage = false
	}
	for _, item := range items {
		binding, err := decode[contract.Binding](item.Binding)
		if err != nil {
			return contract.Detail{}, err
		}
		canRecover := result.CanManage && s.authorize(ctx, actor, contract.Definition{Diagnostic: job.Diagnostic}, binding, job.WritesCloud && !job.CancelRequested) == nil
		view := contract.Item{ID: item.ID, Label: item.Label, Status: item.Status, Binding: binding, Steps: []contract.Step{}, CanRecover: canRecover}
		for _, step := range steps {
			if step.ItemID != item.ID {
				continue
			}
			output, err := decode[contract.Output](step.Result)
			if err != nil {
				return contract.Detail{}, err
			}
			entry := contract.Step{ID: step.ID, Label: step.Label, Status: step.Status, Mode: step.Mode, Code: step.Code, AttemptCount: step.AttemptCount, AttemptLimit: step.AttemptLimit, DeadlineAt: clock.In(step.DeadlineAt), NextRunAt: timeView(step.NextRunAt), Output: output, Attempts: []contract.Attempt{}}
			for _, attempt := range attempts {
				if attempt.StepID != step.ID {
					continue
				}
				binding, err := decode[contract.Binding](attempt.Binding)
				if err != nil {
					return contract.Detail{}, err
				}
				out, err := decode[contract.Output](attempt.Result)
				if err != nil {
					return contract.Detail{}, err
				}
				entry.Attempts = append(entry.Attempts, contract.Attempt{Number: attempt.Number, Mode: attempt.Mode, Outcome: attempt.Outcome, Code: attempt.Code, Binding: binding, Output: out, StartedAt: clock.In(attempt.StartedAt), FinishedAt: timeView(attempt.FinishedAt)})
			}
			view.Steps = append(view.Steps, entry)
		}
		sort.Slice(view.Steps, func(i, j int) bool {
			return findOrdinal(steps, view.Steps[i].ID) < findOrdinal(steps, view.Steps[j].ID)
		})
		result.Items = append(result.Items, view)
	}
	for _, event := range events {
		result.Events = append(result.Events, eventView(event))
	}
	return result, nil
}
func findOrdinal(steps []entity.Step, id string) int {
	for _, step := range steps {
		if step.ID == id {
			return step.Ordinal
		}
	}
	return 0
}
func (s *Service) Events(ctx context.Context, actorID int64, id string, after int64) (contract.EventPage, error) {
	job, _, _, err := s.readJob(ctx, actorID, id, false)
	if err != nil {
		return contract.EventPage{}, err
	}
	if after < 0 || after > job.EventSeq {
		return contract.EventPage{}, contract.ErrInvalid
	}
	rows, err := s.deps.Store.Events(ctx, id, after, 100)
	if err != nil {
		return contract.EventPage{}, err
	}
	result := contract.EventPage{Events: []contract.Event{}, LastSeq: after, Terminal: terminal(job.Status)}
	for _, row := range rows {
		result.Events = append(result.Events, eventView(row))
		result.LastSeq = row.Seq
	}
	return result, nil
}
