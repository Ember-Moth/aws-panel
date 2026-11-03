package repo

import (
	"context"
	"github.com/herta-network/aws-panel/internal/module/inventory/entity"
)

func (s *Store) Overview(ctx context.Context, accounts []string) ([]entity.Overview, error) {
	rows := []entity.Overview{}
	if len(accounts) == 0 {
		return rows, nil
	}
	err := s.db.WithContext(ctx).Raw(`SELECT s.*,
 count(r.id) FILTER (WHERE r.present AND (s.kind<>'ec2_instance' OR r.state<>'terminated')) AS observed_count,
 count(r.id) FILTER (WHERE r.present AND r.state='running') AS running_count,
 count(r.id) FILTER (WHERE r.present AND r.details#>>'{address,kind}'='wavelength') AS carrier_count,
 COALESCE(bool_and(COALESCE(r.details#>>'{address,kind}','')<>'') FILTER (WHERE r.present AND s.kind='ec2_address'),true) AS carrier_known,
 COALESCE(jsonb_agg(DISTINCT COALESCE(r.details#>>'{instance,public_ip}',r.details#>>'{ec2,public_ip}',r.details#>>'{address,ip}')) FILTER (WHERE r.present AND COALESCE(r.details#>>'{address,kind}','')<>'wavelength' AND COALESCE(r.details#>>'{instance,public_ip}',r.details#>>'{ec2,public_ip}',r.details#>>'{address,ip}','')<>'' AND (s.kind<>'ec2_instance' OR r.state<>'terminated')),'[]'::jsonb)::text AS ip_json,
 COALESCE(jsonb_agg(DISTINCT r.region) FILTER (WHERE r.present AND ((s.kind='account_region' AND r.state IN ('ENABLED','ENABLED_BY_DEFAULT')) OR (s.kind='lightsail_region' AND r.state='available'))),'[]'::jsonb)::text AS region_json
 FROM inventory_scopes s LEFT JOIN resource_snapshots r ON r.scope_id=s.id WHERE s.account_id IN ? GROUP BY s.id`, accounts).Scan(&rows).Error
	return rows, convert(err)
}
