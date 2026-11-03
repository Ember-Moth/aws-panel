package service

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/herta-network/aws-panel/internal/auth"
	"github.com/herta-network/aws-panel/internal/auth/vault"
	awsnet "github.com/herta-network/aws-panel/internal/infra/aws"
	"github.com/herta-network/aws-panel/internal/infra/clock"
	"github.com/herta-network/aws-panel/internal/module/accounts/contract"
	"github.com/herta-network/aws-panel/internal/repository"
)

func (s *Service) ProbeProxy(ctx context.Context, actorID int64, id string, meta auth.RequestMeta) (contract.Verification, error) {
	actor, err := s.actor(ctx, actorID, true)
	if err != nil {
		return contract.Verification{}, err
	}
	if !validID(id) {
		return contract.Verification{}, contract.ErrInvalid
	}
	profile, err := s.deps.Store.Proxy(ctx, id)
	if err != nil {
		return contract.Verification{}, err
	}
	if !profile.Active {
		return contract.Verification{}, contract.ErrDisabled
	}
	route := awsnet.Route{Mode: profile.Protocol, Proxy: &awsnet.SOCKS5{Host: profile.Host, Port: profile.Port}}
	if profile.SecretID != nil {
		secret, err := s.deps.Store.Secret(ctx, *profile.SecretID)
		if err != nil {
			return contract.Verification{}, err
		}
		if secret.OwnerType != "proxy" || secret.OwnerID != id || secret.Purpose != "proxy_auth" {
			return contract.Verification{}, contract.ErrVault
		}
		ring, err := vault.Load(s.deps.KeyringFile)
		if err != nil {
			return contract.Verification{}, contract.ErrVault
		}
		raw, err := ring.Open(secret.Binding(), secret.Encrypted())
		if err != nil {
			return contract.Verification{}, contract.ErrVault
		}
		var credentials contract.ProxyAuth
		if json.Unmarshal(raw, &credentials) != nil {
			return contract.Verification{}, contract.ErrVault
		}
		route.Proxy.Username = credentials.Username
		route.Proxy.Password = credentials.Password
	}
	client, err := awsnet.NewHTTPClient(route)
	if err != nil {
		return contract.Verification{}, contract.ErrInvalid
	}
	defer client.CloseIdleConnections()
	client.Transport = &guardedTransport{base: client.Transport, check: func(ctx context.Context) error {
		if _, err := s.actor(ctx, actorID, true); err != nil {
			return err
		}
		current, err := s.deps.Store.Proxy(ctx, id)
		if err != nil {
			return err
		}
		if current.Version != profile.Version || !current.Active {
			return contract.ErrStale
		}
		return nil
	}}
	parent := ctx
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://sts.us-east-1.amazonaws.com/", nil)
	response, callErr := client.Do(request)
	result := contract.Verification{CheckedAt: clock.Now()}
	if response != nil {
		response.Body.Close()
	}
	if callErr == nil {
		result.OK = true
		result.Stage = "tls"
		result.Code = "PROXY_TLS_OK"
		result.Message = "代理到 AWS 公共身份服务的 HTTPS 链路可达；账号身份需单独验证"
	} else {
		result.Stage, result.Code, result.Message = verificationFailure(callErr, true)
	}
	result.CheckedAt = clock.Now()
	persist, persistCancel := context.WithTimeout(parent, 5*time.Second)
	defer persistCancel()
	err = s.deps.Store.InAccountsTx(persist, func(tx repository.AccountsTx) error {
		ctx := persist
		if err := tx.LockAccounts(ctx); err != nil {
			return err
		}
		current, err := tx.Proxy(ctx, id)
		if err != nil {
			return err
		}
		if current.Version != profile.Version {
			return contract.ErrStale
		}
		event := audit(meta, actor, "accounts.proxy.checked", "proxy", id, map[string]string{"stage": result.Stage, "result": result.Code})
		if !result.OK {
			event.Outcome = "failure"
		}
		return tx.AppendAudit(ctx, event)
	})
	return result, err
}
