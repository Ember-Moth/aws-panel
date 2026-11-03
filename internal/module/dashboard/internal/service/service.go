package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/Ember-Moth/aws-panel/internal/auth"
	"github.com/Ember-Moth/aws-panel/internal/infra/clock"
	ac "github.com/Ember-Moth/aws-panel/internal/module/accounts/contract"
	"github.com/Ember-Moth/aws-panel/internal/module/dashboard/contract"
	"github.com/Ember-Moth/aws-panel/internal/module/dashboard/entity"
	iv "github.com/Ember-Moth/aws-panel/internal/module/inventory/contract"
	oc "github.com/Ember-Moth/aws-panel/internal/module/operations/contract"
	"github.com/Ember-Moth/aws-panel/internal/repository"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/google/uuid"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
)

const Operation = "dashboard.refresh"

var regionPattern = regexp.MustCompile(`^[a-z]{2}(?:-[a-z0-9]+)+-[0-9]+$`)

func validRegion(v string) bool {
	return v == "" || len(v) <= 40 && regionPattern.MatchString(v) && !strings.HasPrefix(v, "cn-") && !strings.HasPrefix(v, "us-gov-")
}
func hash(v string) string { sum := sha256.Sum256([]byte(v)); return hex.EncodeToString(sum[:]) }
func raw(v any) string     { r, _ := json.Marshal(v); return string(r) }

type Access interface {
	Principal(context.Context, int64) (auth.Principal, error)
}
type Accounts interface {
	ListAccounts(context.Context, int64) ([]ac.Account, error)
	AccountDetail(context.Context, int64, string) (*ac.AccountDetail, error)
}
type Clients interface {
	ConfigForBound(context.Context, int64, string, string, bool, string, func(context.Context) error) (aws.Config, error)
}
type Inventory interface {
	Overview(context.Context, int64) ([]iv.Observation, error)
}
type Jobs interface {
	Prepare(context.Context, int64, oc.PlanRequest) (oc.Plan, error)
	Submit(context.Context, int64, oc.SubmitRequest, auth.RequestMeta) (oc.Job, error)
}
type Deps struct {
	Access    Access
	Accounts  Accounts
	Inventory Inventory
	Store     repository.DashboardStore
	Jobs      Jobs
}
type Service struct{ deps Deps }

func New(d Deps) *Service { return &Service{deps: d} }
func (s *Service) accounts(ctx context.Context, actor int64, q contract.Query) ([]contract.Account, []contract.Account, error) {
	if _, err := s.deps.Access.Principal(ctx, actor); err != nil {
		return nil, nil, contract.ErrForbidden
	}
	if !validRegion(q.Region) {
		return nil, nil, contract.ErrInvalid
	}
	if q.AccountID != "" {
		if _, err := uuid.Parse(q.AccountID); err != nil {
			return nil, nil, contract.ErrInvalid
		}
	}
	values, err := s.deps.Accounts.ListAccounts(ctx, actor)
	if err != nil {
		return nil, nil, translate(err)
	}
	all := []contract.Account{}
	selected := []contract.Account{}
	found := q.AccountID == ""
	for _, a := range values {
		row := contract.Account{ID: a.ID, Name: a.Name, AWSAccountID: a.AWSAccountID, Active: a.Active, RouteMode: a.DefaultRouteMode, ProxyName: a.DefaultProxyName, Status: "needs_verification"}
		detail, err := s.deps.Accounts.AccountDetail(ctx, actor, a.ID)
		if err != nil {
			return nil, nil, translate(err)
		}
		for _, c := range detail.Connections {
			if c.ID == detail.PreferredConnectionID && c.Status == "verified" {
				row.ConnectionID = c.ID
				row.ConnectionName = c.Name
				break
			}
		}
		if !a.Active {
			row.Status = "disabled"
			row.ConnectionID = ""
		} else if a.Partition != "aws" {
			row.Status = "unsupported"
			row.ConnectionID = ""
		} else if row.ConnectionID != "" {
			row.Status = "ready"
		}
		all = append(all, row)
		if q.AccountID == "" || q.AccountID == a.ID {
			selected = append(selected, row)
			found = true
		}
	}
	if !found {
		return nil, nil, contract.ErrForbidden
	}
	return all, selected, nil
}
func translate(err error) error {
	if err == nil {
		return nil
	}
	var own *contract.Error
	if errors.As(err, &own) {
		return own
	}
	var account *ac.Error
	if errors.As(err, &account) {
		return &contract.Error{Code: account.Code, Message: account.Message}
	}
	var op *oc.Error
	if errors.As(err, &op) {
		return &contract.Error{Code: op.Code, Message: op.Message}
	}
	return contract.ErrUnavailable
}
func key(account, region, kind string) string { return account + "/" + region + "/" + kind }
func minTime(a, b *time.Time) *time.Time {
	if b == nil {
		return a
	}
	v := clock.In(*b)
	if a == nil || v.Before(*a) {
		return &v
	}
	return a
}
func (s *Service) Read(ctx context.Context, actor int64, q contract.Query) (contract.Overview, error) {
	options, accounts, err := s.accounts(ctx, actor, q)
	if err != nil {
		return contract.Overview{}, err
	}
	view := contract.Overview{AccountOptions: options, Accounts: accounts, Query: q, CheckedAt: clock.Now(), Warnings: []string{}, Regions: []string{}, Metrics: []contract.Metric{}, Quotas: []contract.PoolQuota{}}
	ids := []string{}
	visible := map[string]bool{}
	for _, a := range accounts {
		ids = append(ids, a.ID)
		visible[a.ID] = true
		view.AccountCount++
		if a.Active {
			view.ActiveAccounts++
		}
		if a.ConnectionID != "" {
			view.ReadyAccounts++
		}
	}
	snapshots, err := s.deps.Store.Snapshots(ctx, ids)
	if err != nil {
		return view, translate(err)
	}
	merged := map[string]entity.Snapshot{}
	legacy, err := s.deps.Inventory.Overview(ctx, actor)
	if err != nil {
		view.Warnings = append(view.Warnings, "已有资源快照暂时无法读取，仪表盘保留独立采集的数据。")
	}
	lsRegions := map[string]iv.Observation{}
	for _, row := range legacy {
		if row.Kind == iv.LightsailRegion && visible[row.AccountID] && row.LastSuccessAt != nil {
			lsRegions[row.AccountID] = row
		}
	}
	kinds := map[string]string{iv.AccountRegion: contract.Catalog, iv.EC2Instance: contract.EC2Instances, iv.LightsailInstance: contract.LightsailInstances, iv.EC2Address: contract.EC2IPs, iv.LightsailStaticIP: contract.LightsailIPs}
	for _, row := range legacy {
		kind, ok := kinds[row.Kind]
		if !ok || !visible[row.AccountID] {
			continue
		}
		data := contract.Data{Count: row.Count, Running: row.Running, IPv4: row.IPv4}
		if kind == contract.EC2IPs {
			data.CarrierCount = row.CarrierCount
			data.CarrierKnown = row.CarrierKnown
			if row.CarrierKnown {
				data.Count -= row.CarrierCount
			}
		}
		if kind == contract.Catalog {
			data.Regions = row.Regions
			if ls, ok := lsRegions[row.AccountID]; ok {
				data.LightsailKnown = true
				data.LightsailRegions = ls.Regions
			}
		}
		attempt := time.Time{}
		if row.LastAttemptAt != nil {
			attempt = *row.LastAttemptAt
		}
		value := entity.Snapshot{AccountID: row.AccountID, Region: row.Region, Kind: kind, Status: row.Status, Code: row.Code, Data: raw(data), LastAttemptAt: attempt, LastSuccessAt: row.LastSuccessAt}
		merged[key(row.AccountID, row.Region, kind)] = value
	}
	for _, row := range snapshots {
		k := key(row.AccountID, row.Region, row.Kind)
		old, ok := merged[k]
		if !ok || old.LastSuccessAt == nil || row.LastSuccessAt != nil && !row.LastSuccessAt.Before(*old.LastSuccessAt) {
			merged[k] = row
		} else if row.LastAttemptAt.After(old.LastAttemptAt) && row.Status == "failed" {
			old.Status = "failed"
			old.Code = row.Code
			old.LastAttemptAt = row.LastAttemptAt
			merged[k] = old
		}
	}
	return summarize(view, merged), nil
}
func (s *Service) Refresh(ctx context.Context, actor int64, req contract.RefreshRequest, meta auth.RequestMeta) (contract.Refresh, error) {
	if _, err := uuid.Parse(req.IdempotencyKey); err != nil {
		return contract.Refresh{}, contract.ErrInvalid
	}
	_, accounts, err := s.accounts(ctx, actor, contract.Query{AccountID: req.AccountID, Region: req.Region})
	if err != nil {
		return contract.Refresh{}, err
	}
	if len(accounts) > 500 {
		return contract.Refresh{}, &contract.Error{Code: "INVALID_INPUT", Message: "一次最多刷新 500 个账号，请先按账号筛选"}
	}
	out := contract.Refresh{Jobs: []contract.RefreshJob{}, Skipped: []contract.Skipped{}}
	eligible := []contract.Account{}
	for _, a := range accounts {
		if a.ConnectionID == "" {
			reason := map[string]string{"disabled": "账号已停用", "unsupported": "此分区暂不支持资源汇总", "needs_verification": "没有已验证的访问连接"}[a.Status]
			out.Skipped = append(out.Skipped, contract.Skipped{AccountID: a.ID, Name: a.Name, Reason: reason})
			continue
		}
		eligible = append(eligible, a)
	}
	if len(eligible) == 0 {
		return out, contract.ErrNoConnection
	}
	sort.Slice(eligible, func(i, j int) bool { return eligible[i].ID < eligible[j].ID })
	var lastErr error
	for start := 0; start < len(eligible); start += 50 {
		end := min(start+50, len(eligible))
		batch := eligible[start:end]
		input := contract.Input{Targets: []contract.Target{}}
		targets := []oc.Target{}
		for _, a := range batch {
			input.Targets = append(input.Targets, contract.Target{ConnectionID: a.ConnectionID, Region: req.Region})
			targets = append(targets, oc.Target{Key: a.ID, Label: a.Name, Binding: oc.Binding{ConnectionID: a.ConnectionID, Region: "us-east-1"}})
		}
		plan, err := s.deps.Jobs.Prepare(ctx, actor, oc.PlanRequest{Operation: Operation, InputVersion: 1, Input: json.RawMessage(raw(input)), Targets: targets})
		if err == nil {
			job, submitErr := s.deps.Jobs.Submit(ctx, actor, oc.SubmitRequest{PlanID: plan.ID, IdempotencyKey: uuid.NewSHA1(uuid.MustParse(req.IdempotencyKey), []byte(raw(input))).String()}, meta)
			err = submitErr
			if err == nil {
				out.Jobs = append(out.Jobs, contract.RefreshJob{ID: job.ID, Accounts: len(batch)})
				continue
			}
		}
		safe := translate(err)
		lastErr = safe
		for _, a := range batch {
			out.Skipped = append(out.Skipped, contract.Skipped{AccountID: a.ID, Name: a.Name, Reason: safe.Error()})
		}
	}
	if len(out.Jobs) == 0 {
		if lastErr != nil {
			return out, lastErr
		}
		return out, contract.ErrUnavailable
	}
	return out, nil
}
func member(values []string, value string) bool { return slices.Contains(values, value) }
