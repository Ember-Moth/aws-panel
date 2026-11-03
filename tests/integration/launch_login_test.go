package integration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/herta-network/aws-panel/internal/auth"
	"github.com/herta-network/aws-panel/internal/auth/unixpassword"
	"github.com/herta-network/aws-panel/internal/module/accounts"
	ac "github.com/herta-network/aws-panel/internal/module/accounts/contract"
	lc "github.com/herta-network/aws-panel/internal/module/lightsail/contract"
	oc "github.com/herta-network/aws-panel/internal/module/operations/contract"
)

func launchTestBinding(target lc.Target, account string) ac.LaunchBinding {
	target.LaunchSecretID = ""
	raw, _ := json.Marshal(target)
	sum := sha256.Sum256(raw)
	return ac.LaunchBinding{AccountID: account, TargetHash: hex.EncodeToString(sum[:])}
}
func TestLaunchRegionDirectoryChoices(t *testing.T) {
	h := newLaunch(t, false)
	ctx := context.Background()
	_, regions := regionServices(t, h.inventoryHarness, false)
	list, err := regions.Catalogue(ctx, h.op.admin, h.connectionA.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Regions) != 2 {
		t.Fatal("region pagination incomplete")
	}
	choices := map[string]bool{}
	for _, value := range list.Regions {
		choices[value.Code] = value.Selectable
	}
	if choices["us-east-1"] || !choices["ap-east-1"] {
		t.Fatal("region status not reflected in selection")
	}
	supported, err := h.serviceLaunch.Regions(ctx, h.op.admin, h.connectionA.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(supported.Items) != 2 || !supported.Items[0].Available || supported.Items[0].Code != "us-east-1" || supported.Items[1].Available {
		t.Fatal("Lightsail supported/enabled region intersection is wrong")
	}
	if _, err := regions.Catalogue(ctx, h.op.admin+100, h.connectionA.ID); err == nil {
		t.Fatal("region directory escaped authorization")
	}
}
func TestLaunchRootPasswordSecretAndLostResponse(t *testing.T) {
	h := newLaunch(t, true)
	ctx := context.Background()
	const password = "Fixture root ' $ ` \\ password 2026!"
	target := h.target("password-instance")
	target.LoginMethod = "root_password"
	target.RootPassword = password
	h.mode(target.Name, "lost_response")
	preview, err := h.serviceLaunch.Prepare(ctx, h.op.admin, lc.CreateRequest{Targets: []lc.CreateTarget{target}})
	if err != nil {
		t.Fatal(err)
	}
	spec := preview.Targets[0].Spec
	if spec.Target.LoginMethod != "root_password" || spec.Target.LaunchSecretID == "" || spec.Target.KeyPairName != "" {
		t.Fatal("password mode still requires a selected SSH key")
	}
	binding := launchTestBinding(spec.Target, h.accountA.ID)
	hash, err := h.accounts.LaunchSecret(ctx, h.op.admin, spec.Target.LaunchSecretID, binding)
	if err != nil || !unixpassword.ValidHash(hash) {
		t.Fatal("launch hash could not be decrypted", err)
	}
	wrong := binding
	wrong.TargetHash = strings.Repeat("0", 64)
	if _, err := h.accounts.LaunchSecret(ctx, h.op.admin, spec.Target.LaunchSecretID, wrong); !errors.Is(err, ac.ErrForbidden) {
		t.Fatal("launch secret was transferable to a different target", err)
	}
	var cipher []byte
	if err := h.op.owner.QueryRow("SELECT ciphertext FROM secrets WHERE id=$1", spec.Target.LaunchSecretID).Scan(&cipher); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(cipher, []byte(password)) || bytes.Contains(cipher, []byte(hash)) {
		t.Fatal("launch credential persisted without encryption")
	}
	var saved string
	if err := h.op.owner.QueryRow("SELECT input::text||items::text FROM plans WHERE id=$1", preview.ID).Scan(&saved); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(saved, password) || strings.Contains(saved, hash) {
		t.Fatal("plan contains password material")
	}
	raw, _ := json.Marshal(preview)
	if strings.Contains(string(raw), password) || strings.Contains(string(raw), hash) {
		t.Fatal("preview exposes password material")
	}
	job, err := h.serviceLaunch.Submit(ctx, h.op.admin, preview.ID, uuid.NewString(), auth.RequestMeta{})
	if err != nil {
		t.Fatal(err)
	}
	detail := h.op.drive(t, job.ID)
	if detail.Job.Status != oc.Succeeded || h.count(target.Name) != 1 {
		t.Fatal("password create was not reconciled exactly once", detail.Job.Status)
	}
	h.cloudLaunch.mu.Lock()
	userData := h.cloudLaunch.userData[target.Name]
	h.cloudLaunch.mu.Unlock()
	if !strings.Contains(userData, hash) || strings.Contains(userData, password) || !strings.Contains(userData, "PermitRootLogin yes") {
		t.Fatal("SDK user data was absent or contained plaintext")
	}
	if _, err := h.accounts.LaunchSecret(ctx, h.op.admin, spec.Target.LaunchSecretID, binding); !errors.Is(err, ac.ErrNotFound) {
		t.Fatal("identified creation retained its launch secret", err)
	}
	raw, _ = json.Marshal(detail)
	if strings.Contains(string(raw), password) || strings.Contains(string(raw), hash) {
		t.Fatal("task response exposes password material")
	}
	rows, err := h.op.owner.Query("SELECT row_to_json(a)::text FROM audit_events a")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(line, password) || strings.Contains(line, hash) {
			t.Fatal("audit leaked root credential")
		}
	}
}
func TestLaunchPasswordValidationCleanupAndKeyRotation(t *testing.T) {
	h := newLaunch(t, false)
	ctx := context.Background()
	target := h.target("invalid-password")
	target.LoginMethod = "root_password"
	target.RootPassword = "short"
	if _, err := h.serviceLaunch.Prepare(ctx, h.op.admin, lc.CreateRequest{Targets: []lc.CreateTarget{target}}); !errors.Is(err, lc.ErrLogin) {
		t.Fatal("weak password accepted", err)
	}
	target.RootPassword = "Valid fixture root password 2026!"
	target.BlueprintID = "app_wordpress"
	if _, err := h.serviceLaunch.Prepare(ctx, h.op.admin, lc.CreateRequest{Targets: []lc.CreateTarget{target}}); !errors.Is(err, lc.ErrPasswordImage) {
		t.Fatal("unsupported image accepted", err)
	}
	target.BlueprintID = "debian_12"
	target.IPType = "ipv6"
	if _, err := h.serviceLaunch.Prepare(ctx, h.op.admin, lc.CreateRequest{Targets: []lc.CreateTarget{target}}); !errors.Is(err, lc.ErrCatalogue) {
		t.Fatal("invalid bundle unexpectedly accepted", err)
	}
	var count int
	if err := h.op.owner.QueryRow("SELECT count(*) FROM secrets WHERE owner_type='instance_launch'").Scan(&count); err != nil || count != 0 {
		t.Fatal("failed preview kept its launch credential", err)
	}
	target.IPType = "dualstack"
	preview, err := h.serviceLaunch.Prepare(ctx, h.op.admin, lc.CreateRequest{Targets: []lc.CreateTarget{target}})
	if err != nil {
		t.Fatal(err)
	}
	spec := preview.Targets[0].Spec
	binding := launchTestBinding(spec.Target, h.accountA.ID)
	hash, err := h.accounts.LaunchSecret(ctx, h.op.admin, spec.Target.LaunchSecretID, binding)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := accounts.NewMaintenance(h.accountStore, h.keyringPath).Maintain(ctx, "rotate", ""); err != nil {
		t.Fatal("launch secret prevented vault rotation", err)
	}
	after, err := h.accounts.LaunchSecret(ctx, h.op.admin, spec.Target.LaunchSecretID, binding)
	if err != nil || after != hash {
		t.Fatal("vault rotation broke launch credential", err)
	}
	target.LaunchSecretID = spec.Target.LaunchSecretID
	if _, err := h.serviceLaunch.Prepare(ctx, h.op.admin, lc.CreateRequest{Targets: []lc.CreateTarget{target}}); !errors.Is(err, lc.ErrLogin) {
		t.Fatal("client injected a reusable launch credential reference", err)
	}
	if err := h.accounts.Delete(ctx, h.op.admin, "connection", h.connectionA.ID, h.connectionA.Version, auth.RequestMeta{}); err != nil {
		t.Fatal(err)
	}
	if err := h.accounts.Delete(ctx, h.op.admin, "aws_account", h.accountA.ID, h.accountA.Version, auth.RequestMeta{}); err != nil {
		t.Fatal(err)
	}
	if err := h.op.owner.QueryRow("SELECT count(*) FROM secrets WHERE owner_type='instance_launch'").Scan(&count); err != nil || count != 0 {
		t.Fatal("account deletion retained launch secrets", err)
	}
}
