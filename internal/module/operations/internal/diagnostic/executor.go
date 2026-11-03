package diagnostic

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/Ember-Moth/aws-panel/internal/module/operations/contract"
	"github.com/Ember-Moth/aws-panel/internal/repository"
)

type Executor struct{ store repository.OperationsStore }

func New(store repository.OperationsStore) *Executor { return &Executor{store: store} }
func (*Executor) Definition() contract.Definition {
	return contract.Definition{ID: "system.diagnostic", Version: 1, InputVersion: 1, Title: "任务系统自检", Diagnostic: true, MaxAttempts: 10, Deadline: 5 * time.Minute, CallTimeout: 10 * time.Second}
}
func (*Executor) Validate(raw json.RawMessage) (json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var value contract.DiagnosticRequest
	if decoder.Decode(&value) != nil {
		return nil, contract.ErrInvalid
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return nil, contract.ErrInvalid
	}
	if value.Count < 1 || value.Count > 8 {
		return nil, contract.ErrInvalid
	}
	switch value.Behavior {
	case "success", "partial", "retry", "wait", "response_lost":
	default:
		return nil, contract.ErrInvalid
	}
	data, err := json.Marshal(value)
	return data, err
}
func (e *Executor) Plan(ctx context.Context, raw json.RawMessage, _ []contract.Target) ([]contract.ItemPlan, error) {
	var value contract.DiagnosticRequest
	if json.Unmarshal(raw, &value) != nil {
		return nil, contract.ErrInvalid
	}
	items := []contract.ItemPlan{}
	for i := 0; i < value.Count; i++ {
		behavior := value.Behavior
		if behavior == "partial" {
			behavior = "success"
			if i%2 == 1 {
				behavior = "failed"
			}
		}
		input, _ := json.Marshal(struct {
			Behavior string `json:"behavior"`
		}{behavior})
		items = append(items, contract.ItemPlan{Target: contract.Target{Key: fmt.Sprintf("target-%d", i+1), Label: fmt.Sprintf("自检目标 %d", i+1)}, Steps: []contract.StepPlan{{Kind: "apply", Label: "提交本地模拟操作", Input: input}, {Kind: "observe", Label: "确认步骤推进", Input: json.RawMessage(`{}`)}}})
	}
	return items, nil
}
func (e *Executor) Execute(ctx context.Context, call contract.Call) contract.Result {
	if err := call.Check(ctx); err != nil {
		return contract.Result{State: contract.Retry, Code: "GUARD_REJECTED"}
	}
	if call.Kind == "observe" {
		if len(call.Completed) != 1 || call.Completed[0].Output.ResourceID == "" {
			return contract.Result{State: contract.Failed, Code: "PRIOR_RESULT_MISSING"}
		}
		return contract.Result{State: contract.Succeeded, Code: "STEP_CONFIRMED", Output: contract.Output{Summary: "后续步骤已完成"}}
	}
	var input struct {
		Behavior string `json:"behavior"`
	}
	if json.Unmarshal(call.Input, &input) != nil {
		return contract.Result{State: contract.Failed, Code: "INVALID_INPUT"}
	}
	if input.Behavior == "failed" {
		return contract.Result{State: contract.Failed, Code: "SIMULATED_FAILURE"}
	}
	if input.Behavior == "retry" && call.Attempt == 1 {
		return contract.Result{State: contract.Retry, Code: "SIMULATED_TRANSIENT", After: time.Second}
	}
	delay := time.Duration(0)
	if input.Behavior == "wait" {
		delay = 2 * time.Second
	}
	if _, err := e.store.DiagnosticApply(ctx, call.Token, delay); err != nil {
		return contract.Result{State: contract.Waiting, Code: "RESULT_UNCONFIRMED"}
	}
	if input.Behavior == "response_lost" {
		return contract.Result{State: contract.Waiting, Code: "SIMULATED_RESPONSE_LOST", After: time.Second}
	}
	if delay > 0 {
		return contract.Result{State: contract.Waiting, Code: "SIMULATED_PENDING", After: time.Second, Output: contract.Output{ResourceID: "diagnostic/" + call.Token}}
	}
	return contract.Result{State: contract.Succeeded, Code: "SIMULATED_SUCCESS", Output: contract.Output{ResourceID: "diagnostic/" + call.Token, Summary: "本地模拟操作完成；未调用 AWS"}}
}
func (e *Executor) Reconcile(ctx context.Context, call contract.Call) contract.Result {
	if err := call.Check(ctx); err != nil {
		return contract.Result{State: contract.Waiting, Code: "GUARD_REJECTED"}
	}
	if call.Kind == "observe" {
		return contract.Result{State: contract.Succeeded, Code: "STEP_CONFIRMED"}
	}
	ready, err := e.store.DiagnosticEffect(ctx, call.Token)
	if err != nil {
		return contract.Result{State: contract.Waiting, Code: "RECONCILE_UNAVAILABLE"}
	}
	// The simulator has a durable token uniqueness constraint, so retrying
	// after a missing result cannot duplicate its simulated effect.
	if ready == nil {
		return contract.Result{State: contract.Retry, Code: "SIMULATED_ABSENT"}
	}
	if ready.After(time.Now()) {
		return contract.Result{State: contract.Waiting, Code: "SIMULATED_PENDING", After: time.Second}
	}
	return contract.Result{State: contract.Succeeded, Code: "SIMULATED_RECONCILED", Output: contract.Output{ResourceID: "diagnostic/" + call.Token, Summary: "已核对模拟结果；未重复提交"}}
}
