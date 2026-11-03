package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/google/uuid"
	"github.com/herta-network/aws-panel/internal/auth"
	"github.com/herta-network/aws-panel/internal/config"
	"github.com/herta-network/aws-panel/internal/infra/postgres"
	"github.com/herta-network/aws-panel/internal/module/lightsail"
	lc "github.com/herta-network/aws-panel/internal/module/lightsail/contract"
	"github.com/herta-network/aws-panel/internal/module/operations"
	oc "github.com/herta-network/aws-panel/internal/module/operations/contract"
	"github.com/herta-network/aws-panel/internal/repository"
	"github.com/herta-network/aws-panel/internal/repository/kernel"
)

type launchAWS struct {
	ports          map[string][]any
	firewallOps    map[string][]any
	firewallCalls  map[string]int
	firewallInputs map[string]map[string]any
	addresses      *addressAWS
	mu             sync.Mutex
	instances      map[string]map[string]any
	operations     map[string]map[string]any
	modes          map[string]string
	creates        map[string]int
	polls          map[string]int
	price          float64
	quota          float64
	quotaDenied    bool
	userData       map[string]string
}

func newLaunchAWS() *launchAWS {
	return &launchAWS{ports: map[string][]any{}, firewallOps: map[string][]any{}, firewallCalls: map[string]int{}, firewallInputs: map[string]map[string]any{}, addresses: newAddressAWS(), instances: map[string]map[string]any{}, operations: map[string]map[string]any{}, modes: map[string]string{}, creates: map[string]int{}, polls: map[string]int{}, userData: map[string]string{}, price: 5, quota: 100}
}
func (f *launchAWS) Regions(ctx context.Context, cfg aws.Config) ([]lc.Region, error) {
	cfg.BaseEndpoint = aws.String("http://aws-fixture.invalid")
	return lightsail.NewCloud().Regions(ctx, cfg)
}
func (f *launchAWS) Catalogue(ctx context.Context, cfg aws.Config) (lc.Catalogue, error) {
	cfg.BaseEndpoint = aws.String("http://aws-fixture.invalid")
	return lightsail.NewCloud().Catalogue(ctx, cfg)
}
func (f *launchAWS) Instance(ctx context.Context, cfg aws.Config, name string) (*lightsail.Instance, error) {
	cfg.BaseEndpoint = aws.String("http://aws-fixture.invalid")
	return lightsail.NewCloud().Instance(ctx, cfg, name)
}
func (f *launchAWS) Create(ctx context.Context, cfg aws.Config, target lc.Target, tags map[string]string, userData string) (lightsail.Created, error) {
	cfg.BaseEndpoint = aws.String("http://aws-fixture.invalid")
	return lightsail.NewCloud().Create(ctx, cfg, target, tags, userData)
}
func (f *launchAWS) Operations(ctx context.Context, cfg aws.Config, name string) ([]lightsail.Operation, error) {
	cfg.BaseEndpoint = aws.String("http://aws-fixture.invalid")
	return lightsail.NewCloud().Operations(ctx, cfg, name)
}
func (f *launchAWS) handle(w http.ResponseWriter, r *http.Request, account, region, service string, input map[string]any) bool {
	if service != "lightsail" && service != "servicequotas" {
		return false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if service == "lightsail" {
		_, action, _ := strings.Cut(r.Header.Get("X-Amz-Target"), ".")
		name, _ := input["resourceName"].(string)
		opID, _ := input["operationId"].(string)
		if strings.Contains(action, "StaticIp") || action == "GetOperation" && f.addresses.operations[opID] != nil || action == "GetOperationsForResource" && strings.HasPrefix(name, "panel-ip-") {
			f.addresses.initialized[account+"/"+region+"/"] = true
			f.addresses.instances = f.instances
			return f.addresses.handle(w, r, account, region, service, input)
		}
	}
	write := func(value any) { json.NewEncoder(w).Encode(value) }
	if service == "servicequotas" {
		if input["ServiceCode"] != "lightsail" {
			w.WriteHeader(400)
			return true
		}
		if f.quotaDenied {
			w.WriteHeader(403)
			write(map[string]any{"__type": "AccessDeniedException", "Message": "fixture permission denied"})
			return true
		}
		write(map[string]any{"Quotas": []any{map[string]any{"QuotaName": "Instances", "Description": "Maximum vCPUs per Region for Lightsail instances", "ServiceCode": "lightsail", "Value": f.quota, "GlobalQuota": false, "Unit": "None"}, map[string]any{"QuotaName": "Static IPs", "Value": 50, "GlobalQuota": false}}})
		return true
	}
	action := r.Header.Get("X-Amz-Target")
	switch {
	case strings.HasSuffix(action, ".GetInstancePortStates"):
		name, _ := input["instanceName"].(string)
		key := account + "/" + region + "/" + name
		values := f.ports[key]
		if values == nil {
			values = []any{}
		}
		write(map[string]any{"portStates": values})
	case strings.HasSuffix(action, ".PutInstancePublicPorts"):
		name, _ := input["instanceName"].(string)
		key := account + "/" + region + "/" + name
		f.firewallCalls[name]++
		f.firewallInputs[name] = input
		if f.modes[name] == "reject_firewall" {
			w.WriteHeader(400)
			write(map[string]any{"__type": "AccessDeniedException"})
			return true
		}
		values, _ := input["portInfos"].([]any)
		ports := []any{}
		for _, v := range values {
			m := v.(map[string]any)
			m["state"] = "open"
			ports = append(ports, m)
		}
		f.ports[key] = ports
		op := map[string]any{"id": uuid.NewString(), "resourceName": name, "resourceType": "Instance", "operationType": "PutInstancePublicPorts", "status": "Succeeded", "isTerminal": true, "createdAt": float64(time.Now().UnixNano()) / 1e9, "location": map[string]any{"regionName": region}}
		if f.modes[name] == "pending_firewall" {
			op["status"] = "Started"
			op["isTerminal"] = false
		}
		f.firewallOps[key] = append(f.firewallOps[key], op)
		if f.modes[name] == "lost_firewall" {
			w.WriteHeader(502)
			return true
		}
		write(map[string]any{"operation": op})
	case strings.HasSuffix(action, ".GetBlueprints"):
		write(map[string]any{"blueprints": []any{map[string]any{"blueprintId": "debian_12", "name": "Debian 12", "version": "12.9", "isActive": true, "platform": "LINUX_UNIX", "type": "os", "minPower": 100}, map[string]any{"blueprintId": "old_os", "name": "Old", "isActive": false, "platform": "LINUX_UNIX", "type": "os"}, map[string]any{"blueprintId": "app_fixture", "name": "App", "isActive": true, "platform": "LINUX_UNIX", "type": "app"}}})
	case strings.HasSuffix(action, ".GetBundles"):
		id, ipv4, price := "small_3_0", 1, f.price
		if input["pageToken"] != nil {
			id, ipv4, price = "small_ipv6_3_0", 0, 3.5
		}
		output := map[string]any{"bundles": []any{map[string]any{"bundleId": id, "name": "Small", "isActive": true, "supportedPlatforms": []string{"LINUX_UNIX"}, "cpuCount": 2, "ramSizeInGb": 2, "diskSizeInGb": 60, "power": 500, "price": price, "publicIpv4AddressCount": ipv4}}}
		if input["pageToken"] == nil {
			output["nextPageToken"] = "bundle-2"
		}
		write(output)
	case strings.HasSuffix(action, ".GetKeyPairs"):
		write(map[string]any{"keyPairs": []any{map[string]any{"name": "fixture-key"}}})
	case strings.HasSuffix(action, ".GetInstances"):
		values := []any{}
		for key, value := range f.instances {
			if strings.HasPrefix(key, account+"/"+region+"/") {
				values = append(values, value)
			}
		}
		write(map[string]any{"instances": values})
	case strings.HasSuffix(action, ".GetInstance"):
		name, _ := input["instanceName"].(string)
		key := account + "/" + region + "/" + name
		value := f.instances[key]
		if value == nil {
			w.WriteHeader(400)
			write(map[string]any{"__type": "NotFoundException", "message": "fixture missing"})
			return true
		}
		f.polls[key]++
		copy := map[string]any{}
		for name, value := range value {
			copy[name] = value
		}
		if f.polls[key] >= 2 {
			copy["state"] = map[string]any{"name": "running", "code": 16}
			value["state"] = copy["state"]
		}
		if f.modes[name] == "wrong_spec" {
			copy["blueprintId"] = "unexpected_blueprint"
		}
		if f.modes[name] == "wrong_tags" {
			copy["tags"] = []any{map[string]any{"key": "aws-panel:step", "value": "another-task"}}
		}
		write(map[string]any{"instance": copy})
	case strings.HasSuffix(action, ".CreateInstances"):
		names, _ := input["instanceNames"].([]any)
		if len(names) != 1 {
			w.WriteHeader(400)
			return true
		}
		name, _ := names[0].(string)
		key := account + "/" + region + "/" + name
		f.creates[name]++
		f.userData[name], _ = input["userData"].(string)
		mode := f.modes[name]
		if mode == "reject" {
			w.WriteHeader(400)
			write(map[string]any{"__type": "AccessDeniedException", "message": "private fixture " + testSecretA})
			return true
		}
		if mode == "drop_before_accept" {
			connection, _, _ := w.(http.Hijacker).Hijack()
			connection.Close()
			return true
		}
		if f.instances[key] != nil {
			w.WriteHeader(400)
			write(map[string]any{"__type": "InvalidInputException", "message": "name conflict"})
			return true
		}
		stamp := float64(time.Now().UnixNano()) / 1e9
		op := map[string]any{"id": uuid.NewString(), "resourceName": name, "resourceType": "Instance", "operationType": "CreateInstance", "status": "Succeeded", "isTerminal": true, "createdAt": stamp, "location": map[string]any{"regionName": region, "availabilityZone": input["availabilityZone"]}}
		if mode == "failed_operation" || mode == "failed_with_resource" {
			op["status"] = "Failed"
			op["errorCode"] = "FixtureFailure"
		}
		if mode == "pending_operation" {
			op["status"] = "Started"
			op["isTerminal"] = false
		}
		f.operations[key] = op
		if mode != "failed_operation" {
			keyPair := input["keyPairName"]
			if keyPair == nil {
				keyPair = "LightsailDefaultKeyPair"
			}
			f.instances[key] = map[string]any{"arn": fmt.Sprintf("arn:aws:lightsail:%s:%s:Instance/%s", region, account, uuid.NewString()), "name": name, "blueprintId": input["blueprintId"], "bundleId": input["bundleId"], "ipAddressType": input["ipAddressType"], "sshKeyName": keyPair, "createdAt": stamp, "location": map[string]any{"regionName": region, "availabilityZone": input["availabilityZone"]}, "state": map[string]any{"name": "pending", "code": 0}, "hardware": map[string]any{"cpuCount": 2, "ramSizeInGb": 2}, "tags": input["tags"]}
		}
		if mode == "lost_response" || mode == "ambiguous_history" {
			connection, _, _ := w.(http.Hijacker).Hijack()
			connection.Close()
			return true
		}
		write(map[string]any{"operations": []any{op}})
	case strings.HasSuffix(action, ".GetOperationsForResource"):
		name, _ := input["resourceName"].(string)
		value := f.operations[account+"/"+region+"/"+name]
		values := []any{}
		if value != nil {
			values = append(values, value)
			if f.modes[name] == "ambiguous_history" {
				other := map[string]any{}
				for key, field := range value {
					other[key] = field
				}
				other["id"] = uuid.NewString()
				values = append(values, other)
			}
		}
		values = append(values, f.firewallOps[account+"/"+region+"/"+name]...)
		write(map[string]any{"operations": values})
	default:
		return false
	}
	return true
}

type launchHarness struct {
	*inventoryHarness
	cloudLaunch   *launchAWS
	creationStore repository.LightsailStore
	serviceLaunch lightsail.Service
}

func newLaunch(t *testing.T, enabled bool) *launchHarness {
	t.Helper()
	h := newInventory(t)
	cloud := newLaunchAWS()
	h.cloud.extra = cloud.handle
	cfg := config.Defaults().Database
	port, err := strconv.Atoi(os.Getenv("AWS_PANEL_TEST_DB_PORT"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Port = port
	cfg.Name = os.Getenv("AWS_PANEL_TEST_DB_NAME")
	if cfg.Name == "" {
		cfg.Name = "aws_panel_test"
	}
	cfg.Password = os.Getenv("AWS_PANEL_TEST_DB_PASSWORD")
	db, pool, err := postgres.Open(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pool.Close() })
	store := lightsail.NewRepository(kernel.Connection{DB: db}, operations.NewExecutionGuard)
	result := &launchHarness{inventoryHarness: h, cloudLaunch: cloud, creationStore: store}
	result.services(t, store, enabled)
	return result
}
func (h *launchHarness) services(t *testing.T, store repository.LightsailStore, enabled bool) {
	t.Helper()
	cloud := h.cloudLaunch
	jobs, err := operations.New(operations.Deps{Store: h.op.store, Access: h.op.members, Connections: inventoryBindings{h.accounts}, Publisher: h.op.queue, CloudWritesEnabled: enabled, Executors: []oc.Executor{lightsail.NewExecutor(lightsail.ExecutorDeps{AddressCloud: newAddressAWS(), Effects: operations.NewEffectStore(h.runtime), Store: store, Clients: h.accounts, Cloud: cloud, LaunchSecrets: h.accounts})}, RedeliveryAfter: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	h.op.service = jobs
	h.serviceLaunch = lightsail.New(lightsail.Deps{Accounts: h.accounts, Access: h.op.members, Jobs: jobs, Cloud: cloud, WritesEnabled: enabled})
}
func (h *launchHarness) target(name string) lc.CreateTarget {
	return lc.CreateTarget{Target: lc.Target{ConnectionID: h.connectionA.ID, Region: "us-east-1", Name: name, Zone: "us-east-1a", BlueprintID: "debian_12", BundleID: "small_3_0", IPType: "dualstack", KeyPairName: "fixture-key"}}
}
func (h *launchHarness) submit(t *testing.T, targets ...lc.CreateTarget) oc.Job {
	t.Helper()
	preview, err := h.serviceLaunch.Prepare(context.Background(), h.op.admin, lc.CreateRequest{Targets: targets})
	if err != nil {
		t.Fatal(err)
	}
	job, err := h.serviceLaunch.Submit(context.Background(), h.op.admin, preview.ID, uuid.NewString(), auth.RequestMeta{})
	if err != nil {
		t.Fatal(err)
	}
	return job
}
