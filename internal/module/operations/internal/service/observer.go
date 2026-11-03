package service

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/herta-network/aws-panel/internal/module/operations/contract"
	"github.com/herta-network/aws-panel/internal/repository"
)

type Observer struct{ store repository.OperationsStore }

func NewObserver(store repository.OperationsStore) *Observer { return &Observer{store: store} }
func (o *Observer) LatestTargets(ctx context.Context, operation string, refs []contract.TargetRef) ([]contract.TargetState, error) {
	if len(refs) > 200 {
		return nil, contract.ErrInvalid
	}
	if len(refs) == 0 {
		return []contract.TargetState{}, nil
	}
	rows, err := o.store.LatestTargets(ctx, operation, encode(refs))
	if err != nil {
		return nil, err
	}
	result := make([]contract.TargetState, 0, len(rows))
	for _, row := range rows {
		result = append(result, contract.TargetState{AccountID: row.AccountID, TargetKey: row.TargetKey, JobID: row.JobID, StepID: row.StepID, Status: row.Status, Code: row.Code, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, NextRunAt: row.NextRunAt})
	}
	return result, nil
}
func (s *Service) Submission(ctx context.Context, actorID int64, operation, idempotency string, inputVersion int, input json.RawMessage) (contract.Job, error) {
	if !identifier.MatchString(operation) || !submissionKey.MatchString(idempotency) {
		return contract.Job{}, contract.ErrInvalid
	}
	if _, err := s.actor(ctx, actorID); err != nil {
		return contract.Job{}, err
	}
	job, err := s.deps.Store.JobByKey(ctx, actorID, operation, idempotency)
	if err != nil {
		return contract.Job{}, err
	}
	canonical := func(raw []byte) ([]byte, error) {
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		var value any
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		return json.Marshal(value)
	}
	want, err := canonical(input)
	if err != nil {
		return contract.Job{}, contract.ErrInvalid
	}
	got, err := canonical([]byte(job.Input))
	if err != nil {
		return contract.Job{}, contract.ErrUnavailable
	}
	if inputVersion != job.InputVersion || !bytes.Equal(want, got) {
		return contract.Job{}, contract.ErrConflict
	}
	if _, _, _, err := s.readJob(ctx, actorID, job.ID, false); err != nil {
		return contract.Job{}, err
	}
	return jobView(job), nil
}
