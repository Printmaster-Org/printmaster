package alerts

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"testing"

	"printmaster/server/storage"
)

func TestAPIChannelLegacyDependencies(t *testing.T) {
	t.Parallel()
	for _, dependent := range []string{"foreign", "global", "shared", "fleet", "policy", "disabled policy"} {
		for _, method := range []string{http.MethodPut, http.MethodDelete} {
			for _, role := range []storage.Role{storage.RoleOperator, storage.RoleAdmin} {
				t.Run(dependent+"/"+method+"/"+string(role), func(t *testing.T) {
					t.Parallel()
					f := newAPIFixture(t)
					ctx := context.Background()
					id := f.ids["a"]["channel"]
					// Seed directly through storage, as historical/admin-created
					// references need protection independent of new API validation.
					if dependent == "policy" || dependent == "disabled policy" {
						_, err := f.store.CreateEscalationPolicy(ctx, &storage.EscalationPolicy{
							Name: "legacy", Enabled: dependent == "policy",
							Steps: []storage.EscalationStep{{ChannelIDs: []int64{f.ids["b"]["channel"]}}, {ChannelIDs: []int64{id}}},
						})
						if err != nil {
							t.Fatal(err)
						}
					} else {
						tenants := map[string][]string{"foreign": {"b"}, "shared": {"a", "b"}, "fleet": {"a"}}[dependent]
						scope := storage.AlertScopeTenant
						if dependent == "fleet" {
							scope = storage.AlertScopeFleet
						}
						_, err := f.store.CreateAlertRule(ctx, &storage.AlertRule{
							Name: "legacy", Type: "custom", Scope: scope, TenantIDs: tenants,
							ChannelIDs: []int64{id}, Enabled: false,
						})
						if err != nil {
							t.Fatal(err)
						}
					}
					before, err := f.store.GetNotificationChannel(ctx, id)
					if err != nil {
						t.Fatal(err)
					}
					rulesBefore, err := f.store.ListAlertRules(ctx)
					if err != nil {
						t.Fatal(err)
					}
					policiesBefore, err := f.store.ListEscalationPolicies(ctx)
					if err != nil {
						t.Fatal(err)
					}
					path := fmt.Sprintf("/api/v1/notification-channels/%d?tenant_id=a", id)
					body := storage.NotificationChannel{Name: "changed", Type: "webhook", Enabled: false,
						TenantIDs: []string{"a"}, ConfigJSON: `{"url":"https://8.8.8.8/changed"}`}
					expectStatus(t, f.request(t, tenantPrincipal(role, "a"), method, path, body), http.StatusForbidden)
					after, err := f.store.GetNotificationChannel(ctx, id)
					if err != nil || !reflect.DeepEqual(before, after) {
						t.Fatalf("denied mutation changed persisted channel: %+v, %v", after, err)
					}
					rulesAfter, err := f.store.ListAlertRules(ctx)
					if err != nil || !reflect.DeepEqual(rulesBefore, rulesAfter) {
						t.Fatalf("denied mutation changed rules: %v", err)
					}
					policiesAfter, err := f.store.ListEscalationPolicies(ctx)
					if err != nil || !reflect.DeepEqual(policiesBefore, policiesAfter) {
						t.Fatalf("denied mutation changed policies: %v", err)
					}
					if f.audits.Load() != 0 || f.sends.Load() != 0 {
						t.Fatal("denied mutation caused side effects")
					}
					want := http.StatusOK
					if method == http.MethodDelete {
						want = http.StatusNoContent
					}
					expectStatus(t, f.request(t, adminPrincipal(), method, path, body), want)
					if method == http.MethodPut {
						after, err = f.store.GetNotificationChannel(ctx, id)
						if err != nil || after.Name != body.Name || after.ConfigJSON != body.ConfigJSON || after.Enabled {
							t.Fatalf("admin update not persisted: %+v, %v", after, err)
						}
					} else if after, _ = f.store.GetNotificationChannel(ctx, id); after != nil {
						t.Fatal("admin deletion not persisted")
					}
					if f.audits.Load() != 1 {
						t.Fatal("admin mutation missing audit")
					}
				})
			}
		}
	}
}

func TestAPIChannelSameTenantDependencies(t *testing.T) {
	t.Parallel()
	for _, method := range []string{http.MethodPut, http.MethodDelete} {
		t.Run(method, func(t *testing.T) {
			t.Parallel()
			f := newAPIFixture(t)
			ctx := context.Background()
			id := f.ids["a"]["channel"]
			rule, err := f.store.GetAlertRule(ctx, f.ids["a"]["rule"])
			if err != nil {
				t.Fatal(err)
			}
			rule.ChannelIDs = []int64{id}
			if err := f.store.UpdateAlertRule(ctx, rule); err != nil {
				t.Fatal(err)
			}
			body := storage.NotificationChannel{Name: "same tenant", Type: "webhook", TenantIDs: []string{"a"}}
			want := http.StatusOK
			if method == http.MethodDelete {
				want = http.StatusNoContent
			}
			expectStatus(t, f.request(t, tenantPrincipal(storage.RoleOperator, "a"), method,
				fmt.Sprintf("/api/v1/notification-channels/%d", id), body), want)
			channel, _ := f.store.GetNotificationChannel(ctx, id)
			if method == http.MethodPut && (channel == nil || channel.Name != body.Name) {
				t.Fatal("same-tenant update not persisted")
			}
			if method == http.MethodDelete && channel != nil {
				t.Fatal("same-tenant deletion not persisted")
			}
		})
	}
}

func TestAPIRuleChannelOwnershipCompatibility(t *testing.T) {
	t.Parallel()
	for _, method := range []string{http.MethodPost, http.MethodPut} {
		for _, owner := range []string{"a", "b", "shared", "global"} {
			t.Run(method+"/"+owner, func(t *testing.T) {
				t.Parallel()
				f := newAPIFixture(t)
				path := "/api/v1/alert-rules"
				want := http.StatusCreated
				if method == http.MethodPut {
					path += fmt.Sprintf("/%d", f.ids["a"]["rule"])
					want = http.StatusOK
				}
				rule := storage.AlertRule{Name: "compatible", Type: "custom", Scope: storage.AlertScopeTenant,
					TenantIDs: []string{"a"}, ChannelIDs: []int64{f.ids[owner]["channel"]}}
				p := tenantPrincipal(storage.RoleOperator, "a", "b")
				if owner == "b" || owner == "global" {
					denied := http.StatusForbidden
					if owner == "global" {
						denied = http.StatusNotFound
					}
					expectStatus(t, f.request(t, p, method, path, rule), denied)
					stored, err := f.store.GetAlertRule(context.Background(), f.ids["a"]["rule"])
					if err != nil || len(stored.ChannelIDs) != 0 || f.audits.Load() != 0 {
						t.Fatalf("denied reference persisted: %+v, %v", stored, err)
					}
				} else {
					expectStatus(t, f.request(t, p, method, path, rule), want)
				}
				// Admin may deliberately create cross-tenant/global references.
				expectStatus(t, f.request(t, adminPrincipal(), method, path, rule), want)
			})
		}
	}
}

func TestAPIChannelOwnershipChangePreservesDependencies(t *testing.T) {
	t.Parallel()
	f := newAPIFixture(t)
	ctx := context.Background()
	id := f.ids["a"]["channel"]
	rule, err := f.store.GetAlertRule(ctx, f.ids["a"]["rule"])
	if err != nil {
		t.Fatal(err)
	}
	rule.ChannelIDs = []int64{id}
	if err := f.store.UpdateAlertRule(ctx, rule); err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/api/v1/notification-channels/%d", id)
	p := tenantPrincipal(storage.RoleOperator, "a", "b")
	body := storage.NotificationChannel{Name: "moved", Type: "webhook", TenantIDs: []string{"b"}}
	expectStatus(t, f.request(t, p, http.MethodPut, path, body), http.StatusForbidden)
	channel, err := f.store.GetNotificationChannel(ctx, id)
	if err != nil || !reflect.DeepEqual(channel.TenantIDs, []string{"a"}) || f.audits.Load() != 0 {
		t.Fatalf("incompatible ownership persisted: %+v, %v", channel, err)
	}
	body.TenantIDs = []string{"a", "b"}
	expectStatus(t, f.request(t, p, http.MethodPut, path, body), http.StatusOK)
	// Losing one tenant from a shared rule's channel is incompatible too.
	rule.TenantIDs = []string{"a", "b"}
	if err := f.store.UpdateAlertRule(ctx, rule); err != nil {
		t.Fatal(err)
	}
	body.TenantIDs = []string{"a"}
	expectStatus(t, f.request(t, p, http.MethodPut, path, body), http.StatusForbidden)
	expectStatus(t, f.request(t, adminPrincipal(), http.MethodPut, path, body), http.StatusOK)
}

func TestAPIPolicyChannelDependenciesRemainAdminOnly(t *testing.T) {
	t.Parallel()
	for _, method := range []string{http.MethodPost, http.MethodPut} {
		t.Run(method, func(t *testing.T) {
			t.Parallel()
			f := newAPIFixture(t)
			path := "/api/v1/escalation-policies"
			want := http.StatusCreated
			if method == http.MethodPut {
				path += fmt.Sprintf("/%d", f.policyID)
				want = http.StatusOK
			}
			policy := storage.EscalationPolicy{Name: "admin dependency", Enabled: true,
				Steps: []storage.EscalationStep{{ChannelIDs: []int64{f.ids["a"]["channel"]}}}}
			expectStatus(t, f.request(t, tenantPrincipal(storage.RoleOperator, "a", "b"), method, path, policy), http.StatusForbidden)
			w := f.request(t, adminPrincipal(), method, path, policy)
			expectStatus(t, w, want)
			created := decodeResponse[storage.EscalationPolicy](t, w)
			stored, err := f.store.GetEscalationPolicy(context.Background(), created.ID)
			if err != nil || !reflect.DeepEqual(stored.Steps, policy.Steps) {
				t.Fatalf("admin policy dependency not persisted: %+v, %v", stored, err)
			}
			channelPath := fmt.Sprintf("/api/v1/notification-channels/%d", f.ids["a"]["channel"])
			expectStatus(t, f.request(t, tenantPrincipal(storage.RoleOperator, "a"), http.MethodDelete, channelPath, nil), http.StatusForbidden)
			expectStatus(t, f.request(t, adminPrincipal(), http.MethodDelete, channelPath, nil), http.StatusNoContent)
		})
	}
}

type dependencyReadFailureStore struct {
	Store
	failRules bool
}

func (s dependencyReadFailureStore) ListAlertRules(ctx context.Context) ([]storage.AlertRule, error) {
	if s.failRules {
		return nil, errors.New("rule dependency lookup failed")
	}
	return s.Store.ListAlertRules(ctx)
}

func (s dependencyReadFailureStore) ListEscalationPolicies(context.Context) ([]storage.EscalationPolicy, error) {
	return nil, errors.New("policy dependency lookup failed")
}

func TestAPIChannelDependencyReadFailsClosed(t *testing.T) {
	t.Parallel()
	for _, failRules := range []bool{true, false} {
		for _, method := range []string{http.MethodPut, http.MethodDelete} {
			t.Run(fmt.Sprintf("rules=%t/%s", failRules, method), func(t *testing.T) {
				t.Parallel()
				f := newAPIFixture(t)
				f.api.store = dependencyReadFailureStore{Store: f.store, failRules: failRules}
				id := f.ids["a"]["channel"]
				before, err := f.store.GetNotificationChannel(context.Background(), id)
				if err != nil {
					t.Fatal(err)
				}
				body := storage.NotificationChannel{Name: "changed", Type: "webhook", TenantIDs: []string{"a"}}
				expectStatus(t, f.request(t, tenantPrincipal(storage.RoleOperator, "a"), method,
					fmt.Sprintf("/api/v1/notification-channels/%d", id), body), http.StatusInternalServerError)
				after, err := f.store.GetNotificationChannel(context.Background(), id)
				if err != nil || !reflect.DeepEqual(before, after) || f.audits.Load() != 0 {
					t.Fatalf("lookup failure allowed mutation: %+v, %v", after, err)
				}
			})
		}
	}
}
