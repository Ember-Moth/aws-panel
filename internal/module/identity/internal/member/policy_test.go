package member

import (
	"github.com/herta-network/aws-panel/internal/module/identity/contract"
	"testing"
)

func TestRoleCapabilitiesDefaultToDeny(t *testing.T) {
	for _, role := range []string{contract.RoleAdmin, contract.RoleOperator, contract.RoleViewer} {
		if !Allowed(role, contract.ActionSystemRead) {
			t.Fatal("member cannot read system status")
		}
		for _, action := range []string{contract.ActionMembersManage, contract.ActionAuditRead, contract.ActionConnectionsManage} {
			if Allowed(role, action) != (role == contract.RoleAdmin) {
				t.Fatal("administrative capability escaped its role")
			}
		}
		if Allowed(role, "unregistered.action") {
			t.Fatal("unknown action is not denied")
		}
		if Allowed(role, contract.ActionCloudOperate) != (role == contract.RoleAdmin || role == contract.RoleOperator) {
			t.Fatal("cloud operation role boundary changed")
		}
	}
	if Allowed("unknown", contract.ActionSystemRead) {
		t.Fatal("unknown role is not denied")
	}
}
