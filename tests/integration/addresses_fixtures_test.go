package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/google/uuid"
	"github.com/herta-network/aws-panel/internal/auth"
	"github.com/herta-network/aws-panel/internal/module/addresses"
	ad "github.com/herta-network/aws-panel/internal/module/addresses/contract"
	"github.com/herta-network/aws-panel/internal/module/operations"
	oc "github.com/herta-network/aws-panel/internal/module/operations/contract"
)

type addressAWS struct {
	mu          sync.Mutex
	ips         map[string]map[string]any
	instances   map[string]map[string]any
	operations  map[string]map[string]any
	history     map[string][]string
	initialized map[string]bool
	calls       map[string]int
	mode        string
	quota       float64
	next        int
}

func newAddressAWS() *addressAWS {
	return &addressAWS{ips: map[string]map[string]any{}, instances: map[string]map[string]any{}, operations: map[string]map[string]any{}, history: map[string][]string{}, initialized: map[string]bool{}, calls: map[string]int{}, quota: 5, next: 20}
}
func addressConfig(cfg aws.Config) aws.Config {
	cfg.BaseEndpoint = aws.String("http://aws-fixture.invalid")
	return cfg
}
func (f *addressAWS) Catalogue(c context.Context, cfg aws.Config, s, a string) (ad.Catalogue, error) {
	return addresses.NewCloud().Catalogue(c, addressConfig(cfg), s, a)
}
func (f *addressAWS) Address(c context.Context, cfg aws.Config, s, a, id string) (*ad.Address, error) {
	return addresses.NewCloud().Address(c, addressConfig(cfg), s, a, id)
}
func (f *addressAWS) Instance(c context.Context, cfg aws.Config, s, a, id string) (*ad.Instance, error) {
	return addresses.NewCloud().Instance(c, addressConfig(cfg), s, a, id)
}
func (f *addressAWS) FindAllocated(c context.Context, cfg aws.Config, s, a, n string, tags map[string]string) ([]ad.Address, error) {
	return addresses.NewCloud().FindAllocated(c, addressConfig(cfg), s, a, n, tags)
}
func (f *addressAWS) History(c context.Context, cfg aws.Config, s, n string) ([]addresses.Operation, error) {
	return addresses.NewCloud().History(c, addressConfig(cfg), s, n)
}
func (f *addressAWS) Operation(c context.Context, cfg aws.Config, s, id string) (*addresses.Operation, error) {
	return addresses.NewCloud().Operation(c, addressConfig(cfg), s, id)
}
func (f *addressAWS) Allocate(c context.Context, cfg aws.Config, s, n, b string, tags map[string]string) (addresses.Accepted, error) {
	return addresses.NewCloud().Allocate(c, addressConfig(cfg), s, n, b, tags)
}
func (f *addressAWS) Attach(c context.Context, cfg aws.Config, s string, a ad.Address, i ad.Instance) (addresses.Accepted, error) {
	return addresses.NewCloud().Attach(c, addressConfig(cfg), s, a, i)
}
func (f *addressAWS) Detach(c context.Context, cfg aws.Config, s string, a ad.Address) (addresses.Accepted, error) {
	return addresses.NewCloud().Detach(c, addressConfig(cfg), s, a)
}
func (f *addressAWS) Release(c context.Context, cfg aws.Config, s string, a ad.Address) (addresses.Accepted, error) {
	return addresses.NewCloud().Release(c, addressConfig(cfg), s, a)
}
func (f *addressAWS) Quota(c context.Context, cfg aws.Config, s, a string) (ad.Quota, error) {
	return addresses.NewCloud().Quota(c, addressConfig(cfg), s, a)
}
func (f *addressAWS) seed(account, region string) {
	scope := account + "/" + region + "/"
	if f.initialized[scope] {
		return
	}
	f.initialized[scope] = true
	stamp := float64(time.Now().Add(-time.Hour).Unix())
	for index, name := range []string{"instance-a", "instance-b"} {
		f.instances[scope+name] = map[string]any{"name": name, "arn": fmt.Sprintf("arn:aws:lightsail:%s:%s:Instance/%s", region, account, uuid.NewString()), "createdAt": stamp, "location": map[string]any{"regionName": region, "availabilityZone": region + "a"}, "state": map[string]any{"name": "running", "code": 16}, "ipAddressType": "dualstack", "publicIpAddress": fmt.Sprintf("127.0.0.%d", index+10), "privateIpAddress": fmt.Sprintf("10.0.0.%d", index+10), "hardware": map[string]any{"cpuCount": 2}}
	}
	f.ips[scope+"old-ip"] = map[string]any{"name": "old-ip", "arn": fmt.Sprintf("arn:aws:lightsail:%s:%s:StaticIp/%s", region, account, uuid.NewString()), "ipAddress": "127.0.1.10", "attachedTo": "instance-a", "isAttached": true, "createdAt": stamp, "location": map[string]any{"regionName": region}}
	f.instances[scope+"instance-a"]["publicIpAddress"] = "127.0.1.10"
}
func (f *addressAWS) handle(w http.ResponseWriter, r *http.Request, account, region, service string, input map[string]any) bool {
	if service != "lightsail" && service != "servicequotas" {
		return false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seed(account, region)
	scope := account + "/" + region + "/"
	write := func(value any) { _ = json.NewEncoder(w).Encode(value) }
	reject := func(code string) {
		w.WriteHeader(400)
		write(map[string]any{"__type": code, "message": "fixture address failure"})
	}
	if service == "servicequotas" {
		write(map[string]any{"Quotas": []any{map[string]any{"QuotaName": "Instances", "Description": "vCPUs per Region", "Value": 100, "GlobalQuota": false}, map[string]any{"QuotaName": "Static IPs", "Value": f.quota, "GlobalQuota": false}}})
		return true
	}
	_, action, _ := strings.Cut(r.Header.Get("X-Amz-Target"), ".")
	name, _ := input["staticIpName"].(string)
	switch action {
	case "GetInstances":
		values := []any{}
		for key, row := range f.instances {
			if strings.HasPrefix(key, scope) {
				values = append(values, row)
			}
		}
		write(map[string]any{"instances": values})
	case "GetInstance":
		id, _ := input["instanceName"].(string)
		row := f.instances[scope+id]
		if row == nil {
			reject("NotFoundException")
		} else {
			write(map[string]any{"instance": row})
		}
	case "GetStaticIps":
		values := []any{}
		for key, row := range f.ips {
			if strings.HasPrefix(key, scope) {
				values = append(values, row)
			}
		}
		write(map[string]any{"staticIps": values})
	case "GetStaticIp":
		row := f.ips[scope+name]
		if row == nil {
			reject("NotFoundException")
		} else {
			write(map[string]any{"staticIp": row})
		}
	case "GetOperationsForResource":
		id, _ := input["resourceName"].(string)
		values := []any{}
		for _, key := range f.history[scope+id] {
			values = append(values, f.operations[key])
		}
		write(map[string]any{"operations": values})
	case "GetOperation":
		id, _ := input["operationId"].(string)
		row := f.operations[id]
		if row == nil {
			reject("NotFoundException")
		} else {
			write(map[string]any{"operation": row})
		}
	case "AllocateStaticIp", "AttachStaticIp", "DetachStaticIp", "ReleaseStaticIp":
		f.calls[action]++
		if f.mode == "reject_attach" && action == "AttachStaticIp" {
			reject("AccessDeniedException")
			return true
		}
		if f.mode == "drop_allocate" && action == "AllocateStaticIp" {
			connection, _, _ := w.(http.Hijacker).Hijack()
			_ = connection.Close()
			return true
		}
		switch action {
		case "AllocateStaticIp":
			if f.ips[scope+name] != nil {
				reject("InvalidInputException")
				return true
			}
			f.next++
			f.ips[scope+name] = map[string]any{"name": name, "arn": fmt.Sprintf("arn:aws:lightsail:%s:%s:StaticIp/%s", region, account, uuid.NewString()), "ipAddress": fmt.Sprintf("127.0.1.%d", f.next), "attachedTo": "", "isAttached": false, "createdAt": float64(time.Now().UnixNano()) / 1e9, "location": map[string]any{"regionName": region}}
		case "AttachStaticIp":
			instance, _ := input["instanceName"].(string)
			address := f.ips[scope+name]
			machine := f.instances[scope+instance]
			if address == nil || machine == nil {
				reject("NotFoundException")
				return true
			}
			if address["isAttached"] == true && address["attachedTo"] != instance {
				reject("InvalidInputException")
				return true
			}
			for key, row := range f.ips {
				if strings.HasPrefix(key, scope) && key != scope+name && row["attachedTo"] == instance {
					reject("InvalidInputException")
					return true
				}
			}
			address["attachedTo"] = instance
			address["isAttached"] = true
			machine["publicIpAddress"] = address["ipAddress"]
		case "DetachStaticIp":
			address := f.ips[scope+name]
			if address == nil {
				reject("NotFoundException")
				return true
			}
			instance, _ := address["attachedTo"].(string)
			if machine := f.instances[scope+instance]; machine != nil {
				machine["publicIpAddress"] = "127.0.0.12"
			}
			address["attachedTo"] = ""
			address["isAttached"] = false
		case "ReleaseStaticIp":
			if row := f.ips[scope+name]; row != nil && row["isAttached"] == true {
				reject("InvalidInputException")
				return true
			}
			delete(f.ips, scope+name)
		}
		id := uuid.NewString()
		operation := map[string]any{"id": id, "resourceName": name, "resourceType": "StaticIp", "operationType": action, "status": "Succeeded", "isTerminal": true, "createdAt": float64(time.Now().UnixNano()) / 1e9, "location": map[string]any{"regionName": region}}
		if f.mode == "pending_attach" && action == "AttachStaticIp" {
			operation["status"] = "Started"
			operation["isTerminal"] = false
		}
		f.operations[id] = operation
		f.history[scope+name] = append(f.history[scope+name], id)
		if f.mode == "lost_responses" {
			connection, _, _ := w.(http.Hijacker).Hijack()
			_ = connection.Close()
			return true
		}
		write(map[string]any{"operations": []any{operation}})
	default:
		return false
	}
	return true
}

type addressHarness struct {
	*inventoryHarness
	cloudAddresses   *addressAWS
	serviceAddresses addresses.Service
}

func newAddresses(t *testing.T, enabled bool) *addressHarness {
	t.Helper()
	h := newInventory(t)
	cloud := newAddressAWS()
	h.cloud.extra = cloud.handle
	effects := operations.NewEffectStore(h.runtime)
	jobs, err := operations.New(operations.Deps{Store: h.op.store, Access: h.op.members, Connections: inventoryBindings{h.accounts}, Publisher: h.op.queue, CloudWritesEnabled: enabled, Executors: []oc.Executor{addresses.NewExecutor(addresses.ExecutorDeps{Clients: h.accounts, Cloud: cloud, Effects: effects})}, RedeliveryAfter: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	h.op.service = jobs
	service := addresses.New(addresses.Deps{Accounts: h.accounts, Access: h.op.members, Jobs: jobs, Cloud: cloud, WritesEnabled: enabled})
	return &addressHarness{inventoryHarness: h, cloudAddresses: cloud, serviceAddresses: service}
}
func (h *addressHarness) target(action string) ad.Target {
	return ad.Target{ConnectionID: h.connectionA.ID, Region: "us-east-1", Service: "lightsail", Action: action}
}
func (h *addressHarness) submit(t *testing.T, target ad.Target) oc.Job {
	t.Helper()
	plan, err := h.serviceAddresses.Prepare(context.Background(), h.op.admin, ad.Request{Targets: []ad.Target{target}})
	if err != nil {
		t.Fatal(err)
	}
	job, err := h.serviceAddresses.Submit(context.Background(), h.op.admin, plan.ID, uuid.NewString(), auth.RequestMeta{})
	if err != nil {
		t.Fatal(err)
	}
	return job
}
func (h *addressHarness) tick(t *testing.T, id string) {
	t.Helper()
	h.op.expedite(t, id)
	if err := h.op.service.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := h.op.service.Publish(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, message := range h.op.queue.take() {
		if err := h.op.service.Run(context.Background(), message); err != nil {
			t.Fatal(err)
		}
	}
}
