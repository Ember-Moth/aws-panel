package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"regexp"
	"sort"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/herta-network/aws-panel/internal/auth"
	"github.com/herta-network/aws-panel/internal/module/operations/contract"
	"github.com/herta-network/aws-panel/internal/module/operations/entity"
	"github.com/herta-network/aws-panel/internal/repository"
)

var submissionKey = regexp.MustCompile(`^[A-Za-z0-9_-]{16,128}$`)

func (s *Service) Submit(ctx context.Context, actorID int64, req contract.SubmitRequest, meta auth.RequestMeta) (contract.Job, error) {
	actor, err := s.actor(ctx, actorID)
	if err != nil {
		return contract.Job{}, err
	}
	if !validID(req.PlanID) || !submissionKey.MatchString(req.IdempotencyKey) {
		return contract.Job{}, contract.ErrInvalid
	}
	plan, err := s.deps.Store.Plan(ctx, req.PlanID)
	if err != nil {
		return contract.Job{}, err
	}
	if plan.ActorID != actor.ID {
		return contract.Job{}, contract.ErrForbidden
	}
	if existing, err := s.deps.Store.JobByKey(ctx, actorID, plan.Operation, req.IdempotencyKey); err == nil {
		if existing.InputHash != plan.InputHash {
			return contract.Job{}, contract.ErrConflict
		}
		if _, _, _, err := s.readJob(ctx, actorID, existing.ID, false); err != nil {
			return contract.Job{}, err
		}
		return jobView(existing), nil
	} else if !errors.Is(err, contract.ErrNotFound) {
		return contract.Job{}, err
	}
	executor := s.executors[key(plan.Operation, plan.ExecutorVersion)]
	if executor == nil {
		return contract.Job{}, contract.ErrExecutor
	}
	d := executor.Definition()
	if d.WritesCloud && !s.deps.CloudWritesEnabled {
		return contract.Job{}, contract.ErrWritesDisabled
	}
	items, err := decode[[]contract.ItemPlan](plan.Items)
	if err != nil {
		return contract.Job{}, err
	}
	for _, item := range items {
		if err := s.authorize(ctx, actor, d, item.Target.Binding, d.WritesCloud); err != nil {
			return contract.Job{}, err
		}
		if !d.Diagnostic {
			current, err := s.inspect(ctx, actorID, item.Target.Binding, d.WritesCloud)
			if err != nil {
				return contract.Job{}, err
			}
			if !sameBinding(current, item.Target.Binding) {
				return contract.Job{}, contract.ErrBinding
			}
		}
	}
	var job entity.Job
	err = s.deps.Store.InOperationsTx(ctx, func(tx repository.OperationsTx) error {
		if err := tx.LockSubmit(ctx, actorID, plan.Operation, req.IdempotencyKey); err != nil {
			return err
		}
		existing, err := tx.JobByKey(ctx, actorID, plan.Operation, req.IdempotencyKey)
		if err == nil {
			if existing.InputHash != plan.InputHash {
				return contract.ErrConflict
			}
			job = existing
			return nil
		}
		if !errors.Is(err, contract.ErrNotFound) {
			return err
		}
		if !d.Diagnostic {
			if err := tx.LockCloudReferences(ctx); err != nil {
				return err
			}
			for _, item := range items {
				if err := tx.CheckBinding(ctx, item.Target.Binding.ConnectionID, item.Target.Binding.Fingerprint); err != nil {
					return err
				}
			}
		}
		now, err := tx.Now(ctx)
		if err != nil {
			return err
		}
		if !plan.ExpiresAt.After(now) {
			return contract.ErrExpired
		}
		job = entity.Job{ID: uuid.NewString(), PlanID: plan.ID, ActorID: actorID, ActorName: actor.Username, Operation: plan.Operation, Title: d.Title, ExecutorVersion: plan.ExecutorVersion, InputVersion: plan.InputVersion, Input: plan.Input, InputHash: plan.InputHash, IdempotencyKey: req.IdempotencyKey, WritesCloud: d.WritesCloud, Diagnostic: d.Diagnostic, Status: contract.Queued, Version: 1, CreatedAt: now, UpdatedAt: now}
		if err := tx.CreateJob(ctx, job); err != nil {
			return err
		}
		for i, itemPlan := range items {
			item := entity.Item{ID: uuid.NewString(), JobID: job.ID, Ordinal: i, TargetKey: itemPlan.Target.Key, Label: itemPlan.Target.Label, ConnectionID: itemPlan.Target.Binding.ConnectionID, Region: itemPlan.Target.Binding.Region, Binding: encode(itemPlan.Target.Binding), Status: contract.Queued}
			locks := map[string]bool{}
			if !d.Diagnostic && itemPlan.Target.LockKey != "" {
				digest := sha256.Sum256([]byte(itemPlan.Target.Binding.Partition + "\x00" + itemPlan.Target.Binding.AWSAccountID + "\x00" + item.Region + "\x00" + itemPlan.Target.LockKey))
				item.LockKey = hex.EncodeToString(digest[:])
				locks[item.LockKey] = true
			}
			if !d.Diagnostic {
				for _, value := range itemPlan.Target.LockKeys {
					digest := sha256.Sum256([]byte(itemPlan.Target.Binding.Partition + "\x00" + itemPlan.Target.Binding.AWSAccountID + "\x00" + item.Region + "\x00" + value))
					locks[hex.EncodeToString(digest[:])] = true
				}
			}
			keys := make([]string, 0, len(locks))
			for value := range locks {
				keys = append(keys, value)
			}
			sort.Strings(keys)
			item.LockKeys = encode(keys)
			if itemPlan.Target.Binding.AccountID != "" {
				id := itemPlan.Target.Binding.AccountID
				item.AccountID = &id
			}
			if err := tx.CreateItem(ctx, item); err != nil {
				return err
			}
			for n, stepPlan := range itemPlan.Steps {
				step := entity.Step{ID: uuid.NewString(), JobID: job.ID, ItemID: item.ID, Ordinal: n, Kind: stepPlan.Kind, Label: stepPlan.Label, Input: string(stepPlan.Input), WritesCloud: stepPlan.WritesCloud, Status: contract.Queued, Mode: "execute", IdempotencyToken: uuid.NewString(), AttemptLimit: d.MaxAttempts, DeadlineAt: now.Add(d.Deadline), Result: "{}", UpdatedAt: now}
				if err := tx.CreateStep(ctx, step); err != nil {
					return err
				}
				if n == 0 {
					if err := schedule(ctx, tx, &step, now, now); err != nil {
						return err
					}
				}
			}
		}
		if err := appendEvent(ctx, tx, &job, nil, "submitted", "", "任务已保存，等待调度", now); err != nil {
			return err
		}
		if err := tx.SaveJob(ctx, job); err != nil {
			return err
		}
		return tx.AppendAudit(ctx, audit(meta, actor, "operations.job.submitted", job.ID, now, map[string]string{"operation": job.Operation, "targets": strconv.Itoa(len(items))}))
	})
	return jobView(job), err
}
func schedule(ctx context.Context, tx repository.OperationsTx, step *entity.Step, at, now time.Time) error {
	step.Generation++
	step.NextRunAt = &at
	step.ScheduledAt = &now
	step.UpdatedAt = now
	if err := tx.SaveStep(ctx, *step); err != nil {
		return err
	}
	return tx.AddOutbox(ctx, entity.Outbox{ID: uuid.NewString(), JobID: step.JobID, StepID: step.ID, Generation: step.Generation, AvailableAt: at, CreatedAt: now})
}
func appendEvent(ctx context.Context, tx repository.OperationsTx, job *entity.Job, step *entity.Step, kind, code, message string, at time.Time) error {
	job.EventSeq++
	job.UpdatedAt = at
	event := entity.Event{JobID: job.ID, Seq: job.EventSeq, Type: kind, Code: code, Message: message, OccurredAt: at}
	if step != nil {
		stepID, itemID := step.ID, step.ItemID
		event.StepID = &stepID
		event.ItemID = &itemID
	}
	return tx.AppendEvent(ctx, event)
}
func clearLease(step *entity.Step) {
	step.LeaseOwner = ""
	step.LeaseExpiresAt = nil
	step.NextRunAt = nil
}
func aggregateItem(steps []entity.Step) string {
	counts := map[string]int{}
	for _, step := range steps {
		counts[step.Status]++
	}
	for _, state := range []string{contract.Running, contract.Waiting, contract.Attention, contract.Failed, contract.Cancelled, contract.Queued} {
		if counts[state] > 0 {
			return state
		}
	}
	return contract.Succeeded
}
func aggregateJob(items []entity.Item) string {
	counts := map[string]int{}
	for _, item := range items {
		counts[item.Status]++
	}
	for _, state := range []string{contract.Running, contract.Waiting, contract.Queued, contract.Attention} {
		if counts[state] > 0 {
			return state
		}
	}
	if counts[contract.Succeeded] == len(items) {
		return contract.Succeeded
	}
	if counts[contract.Succeeded] > 0 {
		return contract.PartialSuccess
	}
	if counts[contract.Cancelled] == len(items) {
		return contract.Cancelled
	}
	return contract.Failed
}
func summarize(ctx context.Context, tx repository.OperationsTx, job *entity.Job, steps []entity.Step, now time.Time) error {
	items, err := tx.Items(ctx, job.ID)
	if err != nil {
		return err
	}
	for i := range items {
		var own []entity.Step
		for _, step := range steps {
			if step.ItemID == items[i].ID {
				own = append(own, step)
			}
		}
		items[i].Status = aggregateItem(own)
		if err := tx.SaveItem(ctx, items[i]); err != nil {
			return err
		}
	}
	state := aggregateJob(items)
	if job.Status != state {
		job.Status = state
		if err := appendEvent(ctx, tx, job, nil, "status", state, "任务状态已更新", now); err != nil {
			return err
		}
	}
	job.UpdatedAt = now
	job.Version++
	job.CompletedAt = nil
	if terminal(state) {
		job.CompletedAt = &now
	}
	return tx.SaveJob(ctx, *job)
}
func blockDescendants(ctx context.Context, tx repository.OperationsTx, steps []entity.Step, step entity.Step, now time.Time) error {
	for i := range steps {
		next := &steps[i]
		if next.ItemID == step.ItemID && next.Ordinal > step.Ordinal && next.Status == contract.Queued {
			next.Status = contract.Cancelled
			next.Code = "DEPENDENCY_FAILED"
			clearLease(next)
			next.UpdatedAt = now
			if err := tx.SaveStep(ctx, *next); err != nil {
				return err
			}
		}
	}
	return nil
}
func (s *Service) Cancel(ctx context.Context, actorID int64, id string, meta auth.RequestMeta) error {
	_, _, actor, err := s.readJob(ctx, actorID, id, true)
	if err != nil {
		return err
	}
	return s.deps.Store.InOperationsTx(ctx, func(tx repository.OperationsTx) error {
		job, err := tx.JobForUpdate(ctx, id)
		if err != nil {
			return err
		}
		if job.CancelRequested || terminal(job.Status) {
			return nil
		}
		steps, err := tx.StepsForUpdate(ctx, id)
		if err != nil {
			return err
		}
		now, err := tx.Now(ctx)
		if err != nil {
			return err
		}
		job.CancelRequested = true
		for i := range steps {
			step := &steps[i]
			if (step.Status == contract.Queued || step.Status == contract.Waiting) && step.Mode == "execute" || step.Status == contract.Attention && !step.Uncertain {
				if step.Uncertain {
					step.Mode = "reconcile"
					step.Status = contract.Waiting
					step.Code = "CANCEL_RECONCILE"
				} else {
					step.Status = contract.Cancelled
					step.Code = "CANCEL_REQUESTED"
					clearLease(step)
				}
				step.UpdatedAt = now
				if err := tx.SaveStep(ctx, *step); err != nil {
					return err
				}
			}
		}
		if err := appendEvent(ctx, tx, &job, nil, "cancel_requested", "CANCEL_REQUESTED", "已停止未执行步骤；已发出的请求继续核对", now); err != nil {
			return err
		}
		if err := summarize(ctx, tx, &job, steps, now); err != nil {
			return err
		}
		return tx.AppendAudit(ctx, audit(meta, actor, "operations.job.cancelled", id, now, nil))
	})
}
func (s *Service) Retry(ctx context.Context, actorID int64, id string, req contract.RetryRequest, meta auth.RequestMeta) error {
	job, items, actor, err := s.readJob(ctx, actorID, id, true)
	if err != nil {
		return err
	}
	if len(req.StepIDs) == 0 || len(req.StepIDs) > 50 {
		return contract.ErrInvalid
	}
	executor := s.executors[key(job.Operation, job.ExecutorVersion)]
	if executor == nil {
		return contract.ErrExecutor
	}
	d := executor.Definition()
	selected := map[string]bool{}
	for _, stepID := range req.StepIDs {
		if !validID(stepID) || selected[stepID] {
			return contract.ErrInvalid
		}
		selected[stepID] = true
	}
	stepsBefore, err := s.deps.Store.Steps(ctx, id)
	if err != nil {
		return err
	}
	selectedItems := map[string]bool{}
	for _, step := range stepsBefore {
		if selected[step.ID] {
			selectedItems[step.ItemID] = true
		}
	}
	bindings := map[string]contract.Binding{}
	// A retry uses the job's original executing member, not the administrator
	// pressing the button. Never silently transfer execution privileges.
	for _, item := range items {
		if !selectedItems[item.ID] {
			continue
		}
		binding, err := decode[contract.Binding](item.Binding)
		if err != nil {
			return err
		}
		if req.RefreshConnections && !d.Diagnostic {
			binding, err = s.inspect(ctx, job.ActorID, binding, d.WritesCloud && !job.CancelRequested)
			if err != nil {
				return err
			}
		}
		if err := s.authorize(ctx, actor, d, binding, d.WritesCloud && !job.CancelRequested); err != nil {
			return err
		}
		bindings[item.ID] = binding
	}
	return s.deps.Store.InOperationsTx(ctx, func(tx repository.OperationsTx) error {
		current, err := tx.JobForUpdate(ctx, id)
		if err != nil {
			return err
		}
		if current.Version != req.Version {
			return contract.ErrConflict
		}
		if !d.Diagnostic {
			if err := tx.LockCloudReferences(ctx); err != nil {
				return err
			}
			for _, binding := range bindings {
				if err := tx.CheckBinding(ctx, binding.ConnectionID, binding.Fingerprint); err != nil {
					return err
				}
			}
		}
		steps, err := tx.StepsForUpdate(ctx, id)
		if err != nil {
			return err
		}
		now, err := tx.Now(ctx)
		if err != nil {
			return err
		}
		byItem := map[string]bool{}
		count := 0
		for i := range steps {
			step := &steps[i]
			if !selected[step.ID] {
				continue
			}
			count++
			if byItem[step.ItemID] || (step.Status != contract.Failed && step.Status != contract.Attention) {
				return contract.ErrConflict
			}
			if current.CancelRequested && step.Status != contract.Attention {
				return contract.ErrConflict
			}
			byItem[step.ItemID] = true
			for _, prior := range steps {
				if prior.ItemID == step.ItemID && prior.Ordinal < step.Ordinal && prior.Status != contract.Succeeded {
					return contract.ErrConflict
				}
			}
			step.Status = contract.Waiting
			step.Mode = "reconcile"
			step.Code = "MANUAL_RECONCILE"
			step.AttemptLimit = step.AttemptCount + d.MaxAttempts
			step.DeadlineAt = now.Add(d.Deadline)
			clearLease(step)
			if err := schedule(ctx, tx, step, now, now); err != nil {
				return err
			}
			if err := appendEvent(ctx, tx, &current, step, "retry_requested", step.Code, "已安排结果核对；历史尝试与成功结果保留", now); err != nil {
				return err
			}
			for n := range steps {
				next := &steps[n]
				if !current.CancelRequested && next.ItemID == step.ItemID && next.Ordinal > step.Ordinal && next.Status == contract.Cancelled && next.Code == "DEPENDENCY_FAILED" {
					next.Status = contract.Queued
					next.Mode = "execute"
					next.Code = ""
					next.DeadlineAt = step.DeadlineAt
					next.UpdatedAt = now
					if err := tx.SaveStep(ctx, *next); err != nil {
						return err
					}
				}
			}
		}
		if count != len(selected) {
			return contract.ErrInvalid
		}
		for _, item := range items {
			if byItem[item.ID] && req.RefreshConnections {
				item.Binding = encode(bindings[item.ID])
				if err := tx.SaveItem(ctx, item); err != nil {
					return err
				}
			}
		}
		if err := summarize(ctx, tx, &current, steps, now); err != nil {
			return err
		}
		return tx.AppendAudit(ctx, audit(meta, actor, "operations.job.retried", id, now, map[string]string{"steps": strconv.Itoa(count), "refresh_connections": strconv.FormatBool(req.RefreshConnections)}))
	})
}
