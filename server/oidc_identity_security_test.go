package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"

	"printmaster/server/storage"
)

func TestOIDCIdentitySecurity(t *testing.T) {
	// Global store is shared by handlers: do not parallelize these tests.
	previous := serverStore
	t.Cleanup(func() { serverStore = previous })
	store := SetupTestStore(t)
	ctx := context.Background()
	for _, id := range []string{"tenant-a", "tenant-b"} {
		if err := store.CreateTenant(ctx, &storage.Tenant{ID: id, Name: id}); err != nil {
			t.Fatal(err)
		}
	}
	for _, tenant := range []string{"", "tenant-a", "tenant-b"} {
		provider := &storage.OIDCProvider{Slug: "provider-" + tenant, Issuer: "https://issuer.example", ClientID: "client", DefaultRole: storage.RoleViewer, TenantID: tenant}
		if err := store.CreateOIDCProvider(ctx, provider); err != nil {
			t.Fatal(err)
		}
		for _, role := range []storage.Role{storage.RoleAdmin, storage.RoleOperator, storage.RoleViewer} {
			name := fmt.Sprintf("%s-%s", provider.Slug, role)
			user := &storage.User{Username: name, Email: name + "@example.com", Role: role, TenantIDs: []string{"tenant-a"}}
			if err := store.CreateUser(ctx, user, "security-test-password"); err != nil {
				t.Fatal(err)
			}
			for _, verified := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/verified=%t", name, verified), func(t *testing.T) {
					claims := &oidcClaims{Subject: fmt.Sprintf("subject-%s-%t", name, verified), Email: user.Email, EmailVerified: verified}
					if got, err := resolveOIDCUser(ctx, provider, claims); err == nil || got != nil {
						t.Fatalf("email collision accepted: user=%+v err=%v", got, err)
					}
					if _, err := store.GetOIDCLink(ctx, provider.Slug, claims.Subject); !errors.Is(err, sql.ErrNoRows) {
						t.Fatalf("denied identity was linked: %v", err)
					}
					if err := store.CreateOIDCLink(ctx, &storage.OIDCLink{ProviderSlug: provider.Slug, Subject: claims.Subject, UserID: user.ID}); err != nil {
						t.Fatal(err)
					}
					// Explicit links remain authority even if email changes/unverified.
					claims.Email = "changed@example.com"
					got, err := resolveOIDCUser(ctx, provider, claims)
					if err != nil || got.ID != user.ID {
						t.Fatalf("prelinked identity broken: user=%+v err=%v", got, err)
					}
				})
			}
		}
		for _, verified := range []bool{false, true} {
			claims := &oidcClaims{Subject: fmt.Sprintf("new-%s-%t", provider.Slug, verified), Email: fmt.Sprintf("new-%s-%t@example.com", provider.Slug, verified), EmailVerified: verified}
			got, err := resolveOIDCUser(ctx, provider, claims)
			if err != nil {
				t.Fatal(err)
			}
			if got.TenantID != tenant || (!verified && got.Email != "") || (verified && got.Email != claims.Email) {
				t.Fatalf("unexpected provisioning scope/email: %+v", got)
			}
		}
		provider.DefaultRole = storage.RoleAdmin
		got, err := resolveOIDCUser(ctx, provider, &oidcClaims{Subject: "new-admin-" + provider.Slug})
		if tenant != "" {
			if err == nil || got != nil {
				t.Fatal("tenant provider provisioned global admin")
			}
		} else if err != nil || got.Role != storage.RoleAdmin {
			t.Fatalf("trusted global admin provisioning broken: %+v %v", got, err)
		}
		if _, err := resolveOIDCUser(ctx, provider, &oidcClaims{}); err == nil {
			t.Fatal("empty subject accepted")
		}
	}
}
