package service

import (
	"context"
	"encoding/json"
	"github.com/herta-network/aws-panel/internal/module/inventory/contract"
)

func (s *Service) Overview(ctx context.Context, actorID int64) ([]contract.Observation, error) {
	accounts, _, err := s.accounts(ctx, actorID)
	if err != nil {
		return nil, err
	}
	rows, err := s.deps.Store.Overview(ctx, ids(accounts))
	if err != nil {
		return nil, err
	}
	result := []contract.Observation{}
	for _, row := range rows {
		value := contract.Observation{CarrierCount: row.CarrierCount, CarrierKnown: row.CarrierKnown, AccountID: row.AccountID, Kind: row.Kind, Region: row.Region, Count: row.ObservedCount, Running: row.RunningCount, Status: row.Status, Code: row.LastErrorCode, LastAttemptAt: timeView(row.LastAttemptAt), LastSuccessAt: timeView(row.LastSuccessAt)}
		if json.Unmarshal([]byte(row.IPJSON), &value.IPv4) != nil || json.Unmarshal([]byte(row.RegionJSON), &value.Regions) != nil {
			return nil, contract.ErrResponse
		}
		result = append(result, value)
	}
	return result, nil
}
