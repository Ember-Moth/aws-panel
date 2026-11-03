package service

import (
	"github.com/Ember-Moth/aws-panel/internal/module/dashboard/contract"
	"github.com/Ember-Moth/aws-panel/internal/module/dashboard/entity"
	"testing"
	"time"
)

func TestLegacyCarrierCountsStayUnknownUntilRefresh(t *testing.T) {
	now := time.Now()
	view := contract.Overview{CheckedAt: now, Accounts: []contract.Account{{ID: "account", Active: true, Status: "ready"}}}
	rows := map[string]entity.Snapshot{
		key("account", "global", contract.Catalog):   {AccountID: "account", Region: "global", Kind: contract.Catalog, LastSuccessAt: &now, Status: "succeeded", Data: `{"regions":["us-east-1"],"lightsail_known":true}`},
		key("account", "us-east-1", contract.EC2IPs): {AccountID: "account", Region: "us-east-1", Kind: contract.EC2IPs, LastSuccessAt: &now, Status: "succeeded", Data: `{"count":4}`},
	}
	result := summarize(view, rows)
	for _, m := range result.Metrics {
		if (m.Key == contract.EC2IPs || m.Key == "carrier_ips") && m.Known {
			t.Fatal("legacy count guessed", m)
		}
	}
	value := rows[key("account", "us-east-1", contract.EC2IPs)]
	value.Data = `{"count":3,"carrier_known":true,"carrier_count":1}`
	rows[key("account", "us-east-1", contract.EC2IPs)] = value
	result = summarize(view, rows)
	seen := 0
	for _, m := range result.Metrics {
		if m.Key == contract.EC2IPs {
			seen++
			if !m.Known || m.Count != 3 {
				t.Fatal(m)
			}
		}
		if m.Key == "carrier_ips" {
			seen++
			if !m.Known || m.Count != 1 {
				t.Fatal(m)
			}
		}
	}
	if seen != 2 {
		t.Fatal("missing metrics")
	}
}
