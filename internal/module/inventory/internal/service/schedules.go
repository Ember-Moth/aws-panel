package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Ember-Moth/aws-panel/internal/auth"
	"github.com/Ember-Moth/aws-panel/internal/infra/clock"
	"github.com/Ember-Moth/aws-panel/internal/module/inventory/contract"
	"github.com/Ember-Moth/aws-panel/internal/module/inventory/entity"
	"github.com/Ember-Moth/aws-panel/internal/module/inventory/internal/executor"
	"github.com/Ember-Moth/aws-panel/internal/module/inventory/internal/scope"
	"github.com/Ember-Moth/aws-panel/internal/repository"
	"github.com/Ember-Moth/aws-panel/internal/repository/kernel"
	"github.com/google/uuid"
)

func scheduleView(row entity.Schedule) (contract.Schedule, error) {
	var input contract.RefreshInput
	if json.Unmarshal([]byte(row.Request), &input) != nil {
		return contract.Schedule{}, contract.ErrUnavailable
	}
	return contract.Schedule{ID: row.ID, Name: row.Name, ActorID: row.ActorID, Targets: input.Targets, IntervalMinutes: row.IntervalSeconds / 60, Active: row.Active, Version: row.Version, NextRunAt: clock.In(row.NextRunAt), LastRunAt: timeView(row.LastRunAt), LastJobID: row.LastJobID, Code: row.LastErrorCode, Message: executor.Message(row.LastErrorCode)}, nil
}
func (s *Service) Schedules(ctx context.Context, actorID int64, page int) (contract.ScheduleList, error) {
	actor, err := s.actor(ctx, actorID)
	if err != nil {
		return contract.ScheduleList{}, err
	}
	if page < 1 || page > 10000 {
		return contract.ScheduleList{}, contract.ErrInvalid
	}
	rows, err := s.deps.Store.Schedules(ctx, actorID, actor.Role == "admin", page, 25)
	if err != nil {
		return contract.ScheduleList{}, err
	}
	result := contract.ScheduleList{Items: []contract.Schedule{}, Page: page}
	if page > 1 {
		result.Previous = page - 1
	}
	if len(rows) > 25 {
		result.Next = page + 1
		rows = rows[:25]
	}
	for _, row := range rows {
		value, err := scheduleView(row)
		if err != nil {
			return contract.ScheduleList{}, err
		}
		owner, ownerErr := s.deps.Access.Principal(ctx, row.ActorID)
		if ownerErr == nil {
			value.ActorName = owner.DisplayName
		} else if errors.Is(ownerErr, auth.ErrDenied) {
			value.ActorName = fmt.Sprintf("已停用成员 #%d", row.ActorID)
		} else {
			return contract.ScheduleList{}, contract.ErrUnavailable
		}
		result.Items = append(result.Items, value)
	}
	return result, nil
}
func (s *Service) Schedule(ctx context.Context, actorID int64, id string) (contract.Schedule, error) {
	actor, err := s.actor(ctx, actorID)
	if err != nil {
		return contract.Schedule{}, err
	}
	if !validID(id) {
		return contract.Schedule{}, contract.ErrInvalid
	}
	row, err := s.deps.Store.Schedule(ctx, id)
	if err != nil {
		return contract.Schedule{}, err
	}
	if row.ActorID != actorID && actor.Role != "admin" {
		return contract.Schedule{}, contract.ErrForbidden
	}
	return scheduleView(row)
}
func (s *Service) validateTargets(ctx context.Context, actorID int64, targets []contract.RefreshTarget) error {
	seen := map[string]bool{}
	for _, target := range targets {
		snapshot, err := s.deps.Accounts.Inspect(ctx, actorID, target.ConnectionID, target.Region, false)
		if err != nil {
			return translate(err)
		}
		if snapshot.Partition != "aws" {
			return contract.ErrPartition
		}
		key := scope.Key(snapshot.AccountID, target.Kind, target.Region)
		if seen[key] {
			return contract.ErrInvalid
		}
		seen[key] = true
	}
	return nil
}
func (s *Service) SaveSchedule(ctx context.Context, actorID int64, id string, req contract.ScheduleRequest, meta auth.RequestMeta) (contract.Schedule, error) {
	actor, err := s.actor(ctx, actorID)
	if err != nil {
		return contract.Schedule{}, err
	}
	req.Name = strings.TrimSpace(req.Name)
	if !textOK(req.Name, 80) || req.IntervalMinutes < 5 || req.IntervalMinutes > 1440 {
		return contract.Schedule{}, contract.ErrInvalid
	}
	targets, err := scope.Normalize(req.Targets)
	if err != nil {
		return contract.Schedule{}, err
	}
	owner := actorID
	if id != "" {
		existing, err := s.Schedule(ctx, actorID, id)
		if err != nil {
			return contract.Schedule{}, err
		}
		owner = existing.ActorID
	}
	if id == "" || req.Active {
		if err := s.validateTargets(ctx, owner, targets); err != nil {
			return contract.Schedule{}, err
		}
	}
	var row entity.Schedule
	err = s.deps.Store.InInventoryTx(ctx, func(tx repository.InventoryTx) error {
		now, err := tx.Now(ctx)
		if err != nil {
			return err
		}
		version := int64(0)
		action := "inventory.schedule.created"
		if id == "" {
			row = entity.Schedule{ID: uuid.NewString(), ActorID: actorID, Version: 1, CreatedAt: now}
		} else {
			row, err = tx.ScheduleForUpdate(ctx, id)
			if err != nil {
				return err
			}
			if row.ActorID != actorID && actor.Role != "admin" {
				return contract.ErrForbidden
			}
			if row.Version != req.Version {
				return contract.ErrConflict
			}
			version = row.Version
			row.Version++
			action = "inventory.schedule.updated"
		}
		row.Name = req.Name
		row.Request = encode(contract.RefreshInput{Targets: targets})
		row.IntervalSeconds = req.IntervalMinutes * 60
		row.Active = req.Active
		row.NextRunAt = now.Add(time.Duration(row.IntervalSeconds) * time.Second)
		row.UpdatedAt = now
		row.LeaseOwner = ""
		row.LeaseUntil = nil
		row.LastErrorCode = ""
		if err := tx.SaveSchedule(ctx, row, version); err != nil {
			return err
		}
		return tx.AppendAudit(ctx, kernel.AuditEntry{ActorID: &actor.ID, ActorName: actor.Username, Action: action, TargetType: "inventory_schedule", TargetID: row.ID, Outcome: "success", RequestID: meta.RequestID, ClientIP: meta.ClientIP, OccurredAt: now, Metadata: map[string]string{"version": strconv.FormatInt(row.Version, 10), "count": strconv.Itoa(len(targets)), "interval_minutes": strconv.Itoa(req.IntervalMinutes), "active_after": strconv.FormatBool(req.Active)}})
	})
	if err != nil {
		return contract.Schedule{}, err
	}
	return scheduleView(row)
}
func (s *Service) DeleteSchedule(ctx context.Context, actorID int64, id string, version int64, meta auth.RequestMeta) error {
	actor, err := s.actor(ctx, actorID)
	if err != nil {
		return err
	}
	if !validID(id) || version < 1 {
		return contract.ErrInvalid
	}
	return s.deps.Store.InInventoryTx(ctx, func(tx repository.InventoryTx) error {
		row, err := tx.ScheduleForUpdate(ctx, id)
		if err != nil {
			return err
		}
		if row.ActorID != actorID && actor.Role != "admin" {
			return contract.ErrForbidden
		}
		if row.Version != version {
			return contract.ErrConflict
		}
		if err := tx.DeleteSchedule(ctx, id, version); err != nil {
			return err
		}
		now, err := tx.Now(ctx)
		if err != nil {
			return err
		}
		return tx.AppendAudit(ctx, kernel.AuditEntry{ActorID: &actor.ID, ActorName: actor.Username, Action: "inventory.schedule.deleted", TargetType: "inventory_schedule", TargetID: id, Outcome: "success", RequestID: meta.RequestID, ClientIP: meta.ClientIP, OccurredAt: now})
	})
}
func (s *Service) Tick(ctx context.Context) error {
	owner := uuid.NewString()
	rows, err := s.deps.Store.ClaimSchedules(ctx, owner, 4, time.Minute)
	if err != nil {
		return err
	}
	var first error
	for _, row := range rows {
		work, cancel := context.WithTimeout(ctx, 10*time.Second)
		current, err := s.deps.Store.Schedule(work, row.ID)
		if err != nil || !current.Active || current.Version != row.Version || current.LeaseOwner != owner {
			cancel()
			continue
		}
		var input contract.RefreshInput
		code := ""
		var jobID *string
		if json.Unmarshal([]byte(row.Request), &input) != nil {
			code = "INVALID_INPUT"
		} else {
			key := fmt.Sprintf("inventory-%s-%d-%d", row.ID, row.Version, row.NextRunAt.UnixMicro())
			job, err := s.Refresh(work, row.ActorID, contract.RefreshRequest{Targets: input.Targets, IdempotencyKey: key}, auth.RequestMeta{RequestID: "inventory-schedule-" + row.ID})
			if err == nil {
				jobID = &job.ID
			} else {
				code = "DEPENDENCY_UNAVAILABLE"
				var fault *contract.Error
				if errors.As(err, &fault) {
					code = fault.Code
				}
			}
		}
		cancel()
		persist, stop := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		err = s.deps.Store.FinishSchedule(persist, row.ID, row.Version, owner, jobID, code)
		stop()
		if err != nil && first == nil {
			first = err
		}
	}
	return first
}
