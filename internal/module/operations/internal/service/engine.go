package service

import (
	"context"
	"errors"
	"time"

	"github.com/Ember-Moth/aws-panel/internal/module/operations/contract"
	"github.com/Ember-Moth/aws-panel/internal/module/operations/entity"
	"github.com/Ember-Moth/aws-panel/internal/repository"
	"github.com/google/uuid"
)

type lease struct {
	job       entity.Job
	item      entity.Item
	step      entity.Step
	executor  contract.Executor
	completed []contract.StepOutput
}

func findStep(steps []entity.Step, id string) *entity.Step {
	for i := range steps {
		if steps[i].ID == id {
			return &steps[i]
		}
	}
	return nil
}
func exhausted(step entity.Step, now time.Time) bool {
	return !step.DeadlineAt.After(now) || step.AttemptCount >= step.AttemptLimit
}
func expireStep(step *entity.Step, now time.Time) {
	step.Status = contract.Failed
	step.Code = "RETRY_LIMIT"
	if !step.DeadlineAt.After(now) {
		step.Code = "DEADLINE_EXCEEDED"
	}
	if step.Mode == "reconcile" || step.Uncertain {
		step.Status = contract.Attention
		step.Code = "RESULT_UNCONFIRMED"
	}
	clearLease(step)
	step.UpdatedAt = now
}
func (s *Service) claim(ctx context.Context, msg contract.Message) (*lease, error) {
	var acquired *lease
	err := s.deps.Store.InOperationsTx(ctx, func(tx repository.OperationsTx) error {
		job, err := tx.JobForUpdate(ctx, msg.JobID)
		if err != nil {
			return err
		}
		if terminal(job.Status) {
			return nil
		}
		steps, err := tx.StepsForUpdate(ctx, job.ID)
		if err != nil {
			return err
		}
		now, err := tx.Now(ctx)
		if err != nil {
			return err
		}
		step := findStep(steps, msg.StepID)
		// An older delivery may wake the current due state. Persisted state and
		// the lease, rather than message age, decide which action is safe.
		if step == nil || msg.Generation > step.Generation || (step.Status != contract.Queued && step.Status != contract.Waiting) || step.NextRunAt == nil || step.NextRunAt.After(now) {
			return nil
		}
		for _, prior := range steps {
			if prior.ItemID == step.ItemID && prior.Ordinal < step.Ordinal && prior.Status != contract.Succeeded {
				return nil
			}
		}
		executor := s.executors[key(job.Operation, job.ExecutorVersion)]
		if job.CancelRequested && step.Mode == "execute" && step.Uncertain {
			step.Mode = "reconcile"
		}
		if job.CancelRequested && step.Mode == "execute" {
			step.Status = contract.Cancelled
			step.Code = "CANCEL_REQUESTED"
			clearLease(step)
		} else if executor == nil {
			step.Status = contract.Attention
			step.Code = "EXECUTOR_UNAVAILABLE"
			clearLease(step)
		} else if exhausted(*step, now) {
			expireStep(step, now)
		}
		if step.Status == contract.Cancelled || step.Status == contract.Attention || step.Status == contract.Failed {
			step.UpdatedAt = now
			if err := tx.SaveStep(ctx, *step); err != nil {
				return err
			}
			if err := blockDescendants(ctx, tx, steps, *step, now); err != nil {
				return err
			}
			if err := appendEvent(ctx, tx, &job, step, "stopped", step.Code, "步骤已停止，未发起新请求", now); err != nil {
				return err
			}
			return summarize(ctx, tx, &job, steps, now)
		}
		items, err := tx.Items(ctx, job.ID)
		if err != nil {
			return err
		}
		var item entity.Item
		for _, candidate := range items {
			if candidate.ID == step.ItemID {
				item = candidate
				break
			}
		}
		if item.ID == "" {
			return contract.ErrUnavailable
		}
		step.Status = contract.Running
		step.AttemptCount++
		step.LeaseEpoch++
		step.LeaseOwner = s.owner
		until := now.Add(s.deps.LeaseDuration)
		step.LeaseExpiresAt = &until
		step.NextRunAt = nil
		step.UpdatedAt = now
		step.Code = ""
		if err := tx.SaveStep(ctx, *step); err != nil {
			return err
		}
		attempt := entity.Attempt{JobID: job.ID, StepID: step.ID, Number: step.AttemptCount, LeaseEpoch: step.LeaseEpoch, Mode: step.Mode, Binding: "{}", StartedAt: now, Outcome: contract.Running, Result: "{}"}
		if err := tx.CreateAttempt(ctx, attempt); err != nil {
			return err
		}
		if err := appendEvent(ctx, tx, &job, step, "attempt_started", step.Mode, "已开始处理步骤", now); err != nil {
			return err
		}
		if err := summarize(ctx, tx, &job, steps, now); err != nil {
			return err
		}
		acquired = &lease{job: job, item: item, step: *step, executor: executor}
		for _, prior := range steps {
			if prior.ItemID == step.ItemID && prior.Ordinal < step.Ordinal && prior.Status == contract.Succeeded {
				output, err := decode[contract.Output](prior.Result)
				if err != nil {
					return err
				}
				acquired.completed = append(acquired.completed, contract.StepOutput{ID: prior.ID, Kind: prior.Kind, Ordinal: prior.Ordinal, Output: output})
			}
		}
		return nil
	})
	return acquired, err
}
func (s *Service) guard(acquired *lease, binding contract.Binding) func(context.Context) error {
	return func(ctx context.Context) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		cancelled, err := s.deps.Store.LeaseState(ctx, acquired.step.ID, acquired.step.LeaseEpoch, s.owner)
		if err != nil {
			return err
		}
		if cancelled && acquired.step.Mode == "execute" {
			return contract.ErrCancelled
		}
		actor, err := s.actor(ctx, acquired.job.ActorID)
		if err != nil {
			return err
		}
		write := acquired.step.WritesCloud && acquired.step.Mode == "execute"
		if write && !s.deps.CloudWritesEnabled {
			return contract.ErrWritesDisabled
		}
		if err := s.authorize(ctx, actor, acquired.executor.Definition(), binding, write); err != nil {
			return err
		}
		if !acquired.job.Diagnostic {
			current, err := s.inspect(ctx, actor.ID, binding, write)
			if err != nil {
				return err
			}
			if !sameBinding(binding, current) {
				return contract.ErrBinding
			}
		}
		return nil
	}
}
func (s *Service) Run(ctx context.Context, msg contract.Message) error {
	if !validID(msg.JobID) || !validID(msg.StepID) || msg.Generation < 1 {
		return contract.ErrInvalid
	}
	acquired, err := s.claim(ctx, msg)
	if errors.Is(err, contract.ErrNotFound) {
		return nil
	}
	if err != nil || acquired == nil {
		return err
	}
	binding, err := decode[contract.Binding](acquired.item.Binding)
	if err != nil {
		return err
	}
	work, cancel := context.WithTimeout(ctx, acquired.executor.Definition().CallTimeout)
	defer cancel()
	work, deadlineCancel := context.WithDeadline(work, acquired.step.DeadlineAt)
	defer deadlineCancel()
	done, heartbeatDone := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(heartbeatDone)
		ticker := time.NewTicker(s.deps.LeaseDuration / 3)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-work.Done():
				return
			case <-ticker.C:
				pulse, pulseCancel := context.WithTimeout(work, s.deps.LeaseDuration/3)
				err := s.deps.Store.RenewLease(pulse, acquired.step.ID, acquired.step.LeaseEpoch, s.owner, s.deps.LeaseDuration)
				pulseCancel()
				if err != nil {
					cancel()
					return
				}
			}
		}
	}()
	defer func() { close(done); <-heartbeatDone }()
	check := s.guard(acquired, binding)
	previous, err := decode[contract.Output](acquired.step.Result)
	if err != nil {
		return err
	}
	call := contract.Call{JobID: acquired.job.ID, ItemID: acquired.item.ID, StepID: acquired.step.ID, ActorID: acquired.job.ActorID, Target: contract.Target{Key: acquired.item.TargetKey, Label: acquired.item.Label, Binding: binding}, Kind: acquired.step.Kind, Input: []byte(acquired.step.Input), Token: acquired.step.IdempotencyToken, Attempt: acquired.step.AttemptCount, Check: check}
	call.Previous = previous
	call.Completed = acquired.completed
	call.LeaseEpoch = acquired.step.LeaseEpoch
	call.LeaseOwner = s.owner
	call.Uncertain = acquired.step.Uncertain
	result := contract.Result{}
	if err = check(work); err == nil {
		err = s.deps.Store.RecordAttemptBinding(work, acquired.step.ID, acquired.step.LeaseEpoch, s.owner, encode(binding))
	}
	if err != nil {
		if errors.Is(err, contract.ErrLease) {
			return nil
		}
		result = contract.Result{State: contract.Attention, Code: "CONNECTION_UNAVAILABLE"}
		var known *contract.Error
		if errors.As(err, &known) {
			result.Code = known.Code
		}
		if errors.Is(err, contract.ErrCancelled) {
			result.State = contract.Cancelled
			if acquired.step.Uncertain {
				result.State = contract.Waiting
				result.Code = "CANCEL_RECONCILE"
			}
		}
		if errors.Is(err, contract.ErrUnavailable) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			result.State = contract.Retry
			result.Code = "DEPENDENCY_UNAVAILABLE"
			if acquired.step.Mode == "reconcile" {
				result.State = contract.Waiting
			}
		}
	} else {
		result = invoke(work, acquired.executor, call, acquired.step.Mode)
	}
	persist, persistCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer persistCancel()
	err = s.finish(persist, acquired, result, err == nil)
	if errors.Is(err, contract.ErrLease) {
		return nil
	}
	return err
}
func invoke(ctx context.Context, executor contract.Executor, call contract.Call, mode string) (result contract.Result) {
	defer func() {
		if recover() != nil {
			result = contract.Result{State: contract.Waiting, Code: "EXECUTOR_INTERRUPTED"}
		}
	}()
	if mode == "reconcile" {
		return executor.Reconcile(ctx, call)
	}
	return executor.Execute(ctx, call)
}
func normalize(result contract.Result) contract.Result {
	switch result.State {
	case contract.Succeeded, contract.Failed, contract.Retry, contract.Waiting, contract.Attention, contract.Cancelled:
	default:
		result.State = contract.Attention
		result.Code = "INVALID_EXECUTOR_RESULT"
	}
	if !codePattern.MatchString(result.Code) {
		result.Code = "OPERATION_RESULT"
	}
	if len(result.Output.ResourceID) > 2048 || len(result.Output.RequestID) > 256 || len(result.Output.Summary) > 2048 {
		result.Output = contract.Output{}
		result.State = contract.Attention
		result.Code = "INVALID_EXECUTOR_RESULT"
	}
	if result.After < time.Second {
		result.After = time.Second
	}
	if result.After > 5*time.Minute {
		result.After = 5 * time.Minute
	}
	return result
}
func (s *Service) finish(ctx context.Context, acquired *lease, result contract.Result, invoked bool) error {
	result = normalize(result)
	return s.deps.Store.InOperationsTx(ctx, func(tx repository.OperationsTx) error {
		job, err := tx.JobForUpdate(ctx, acquired.job.ID)
		if err != nil {
			return err
		}
		steps, err := tx.StepsForUpdate(ctx, job.ID)
		if err != nil {
			return err
		}
		now, err := tx.Now(ctx)
		if err != nil {
			return err
		}
		step := findStep(steps, acquired.step.ID)
		if step == nil || step.Status != contract.Running || step.LeaseEpoch != acquired.step.LeaseEpoch || step.LeaseOwner != s.owner || step.LeaseExpiresAt == nil || !step.LeaseExpiresAt.After(now) {
			return contract.ErrLease
		}
		step.Code = result.Code
		attemptResult := encode(result.Output)
		previous, err := decode[contract.Output](step.Result)
		if err != nil {
			return err
		}
		if result.Output.ResourceID == "" {
			result.Output.ResourceID = previous.ResourceID
		}
		step.Result = encode(result.Output)
		step.UpdatedAt = now
		clearLease(step)
		switch result.State {
		case contract.Retry:
			if result.ConfirmedAbsent {
				step.Uncertain = false
			}
			step.Mode = "execute"
			step.Status = contract.Waiting
			if job.CancelRequested {
				if step.Uncertain {
					step.Mode = "reconcile"
					step.Code = "CANCEL_RECONCILE"
				} else {
					step.Status = contract.Cancelled
					step.Code = "CANCEL_REQUESTED"
				}
			}
		case contract.Waiting:
			step.Uncertain = true
			step.Mode = "reconcile"
			step.Status = contract.Waiting
		case contract.Cancelled:
			step.Status = contract.Cancelled
			if step.Uncertain {
				step.Mode = "reconcile"
				step.Status = contract.Waiting
				step.Code = "CANCEL_RECONCILE"
			}
		default:
			step.Status = result.State
			if result.State == contract.Attention && invoked && step.WritesCloud && !result.ConfirmedAbsent {
				step.Uncertain = true
			}
			if result.State == contract.Attention && result.ConfirmedAbsent {
				step.Uncertain = false
			}
			if result.State == contract.Succeeded || result.State == contract.Failed {
				step.Uncertain = false
			}
		}
		if step.Status == contract.Waiting && exhausted(*step, now) {
			expireStep(step, now)
		}
		if err := tx.FinishAttempt(ctx, step.ID, step.LeaseEpoch, step.Status, step.Code, attemptResult, now); err != nil {
			return err
		}
		if step.Status == contract.Waiting {
			at := now.Add(result.After)
			if err := schedule(ctx, tx, step, at, now); err != nil {
				return err
			}
		} else if err := tx.SaveStep(ctx, *step); err != nil {
			return err
		}
		if step.Status == contract.Succeeded {
			for i := range steps {
				next := &steps[i]
				if next.ItemID != step.ItemID || next.Ordinal != step.Ordinal+1 || next.Status != contract.Queued {
					continue
				}
				if job.CancelRequested {
					next.Status = contract.Cancelled
					next.Code = "CANCEL_REQUESTED"
					next.UpdatedAt = now
					if err := tx.SaveStep(ctx, *next); err != nil {
						return err
					}
					if err := blockDescendants(ctx, tx, steps, *next, now); err != nil {
						return err
					}
				} else if err := schedule(ctx, tx, next, now, now); err != nil {
					return err
				}
			}
		} else if step.Status == contract.Failed || step.Status == contract.Attention || step.Status == contract.Cancelled {
			if err := blockDescendants(ctx, tx, steps, *step, now); err != nil {
				return err
			}
		}
		if err := appendEvent(ctx, tx, &job, step, "attempt_finished", step.Code, "步骤处理结果已保存", now); err != nil {
			return err
		}
		return summarize(ctx, tx, &job, steps, now)
	})
}
func (s *Service) Recover(ctx context.Context) error {
	ids, err := s.deps.Store.DueJobs(ctx, 64, s.deps.RedeliveryAfter)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := s.recoverJob(ctx, id); err != nil {
			return err
		}
	}
	return nil
}
func (s *Service) recoverJob(ctx context.Context, id string) error {
	return s.deps.Store.InOperationsTx(ctx, func(tx repository.OperationsTx) error {
		job, err := tx.JobForUpdate(ctx, id)
		if err != nil {
			return err
		}
		if terminal(job.Status) {
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
		changed := false
		for i := range steps {
			step := &steps[i]
			lost := step.Status == contract.Running && step.LeaseExpiresAt != nil && !step.LeaseExpiresAt.After(now)
			due := (step.Status == contract.Queued || step.Status == contract.Waiting) && step.NextRunAt != nil && !step.NextRunAt.After(now) && step.ScheduledAt != nil && !step.ScheduledAt.Add(s.deps.RedeliveryAfter).After(now)
			if !lost && !due {
				continue
			}
			changed = true
			if lost {
				if err := tx.FinishAttempt(ctx, step.ID, step.LeaseEpoch, "lease_lost", "LEASE_EXPIRED", "{}", now); err != nil {
					return err
				}
				step.LeaseEpoch++
				step.Status = contract.Waiting
				step.Mode = "reconcile"
				step.Uncertain = true
				step.Code = "LEASE_EXPIRED"
				clearLease(step)
			}
			if exhausted(*step, now) {
				expireStep(step, now)
				if err := tx.SaveStep(ctx, *step); err != nil {
					return err
				}
				if err := blockDescendants(ctx, tx, steps, *step, now); err != nil {
					return err
				}
			} else {
				if err := schedule(ctx, tx, step, now, now); err != nil {
					return err
				}
			}
			message := "已补投未完成步骤"
			if lost {
				message = "租约已过期，将先核对结果"
			}
			if err := appendEvent(ctx, tx, &job, step, "recovered", step.Code, message, now); err != nil {
				return err
			}
		}
		if !changed {
			return nil
		}
		return summarize(ctx, tx, &job, steps, now)
	})
}
func (s *Service) Publish(ctx context.Context) error {
	if s.deps.Publisher == nil {
		return contract.ErrUnavailable
	}
	owner := s.owner + "/" + uuid.NewString()
	rows, err := s.deps.Store.ClaimOutbox(ctx, owner, 16, time.Minute)
	if err != nil {
		return err
	}
	var first error
	for _, row := range rows {
		call, cancel := context.WithTimeout(ctx, 2*time.Second)
		err := s.deps.Publisher.Publish(call, row.ID, contract.Message{JobID: row.JobID, StepID: row.StepID, Generation: row.Generation})
		cancel()
		if err != nil && first == nil {
			first = contract.ErrUnavailable
		}
		if err := s.deps.Store.FinishOutbox(ctx, row.ID, owner, err == nil); err != nil && first == nil {
			first = err
		}
	}
	return first
}
